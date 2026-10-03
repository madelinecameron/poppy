// Package hub stores fleet state in a git repository.
//
// Each machine owns the directory machines/<machine>/ and writes only there,
// so concurrent updates from different machines never conflict. Documents are
// idempotent full snapshots rather than deltas, which is what makes the
// publish cycle safe: discarding un-pushed local commits in favour of the
// remote loses nothing that the next scan does not immediately rewrite.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/gitx"
	"github.com/madelinecameron/poppy/internal/model"
)

// Branch is the branch holding state documents.
const Branch = "state"

// PushRetries bounds how many times a push race is retried.
const PushRetries = 3

// Store is a local clone of the hub repository.
type Store struct {
	Dir    string
	Remote string
	git    *gitx.Runner
}

// Open prepares a local hub repository at dir, creating it when absent.
// A store with an empty remote is local-only: state is recorded and queryable
// on this machine but never shared.
func Open(ctx context.Context, dir, remote string) (*Store, error) {
	s := &Store{Dir: dir, Remote: remote, git: gitx.New(dir)}
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenRead prepares a read-only view of the hub repository at dir. It creates
// nothing and writes nothing: no directory, no clone, and none of the git
// identity Open pins. A reader that had to write first would fail on any
// account that cannot, which is exactly the account a status pane runs as, and
// it would leave a git repository behind on a machine that only ever looked.
//
// The returned store can Pull and Fleet, which is the whole read surface.
// Publish needs the identity, so writers must still use Open.
func OpenRead(dir, remote string) *Store {
	return &Store{Dir: dir, Remote: remote, git: gitx.New(dir)}
}

// Exists reports whether a hub repository is present. A missing hub is not an
// empty fleet: it means this machine has never recorded anything, and a
// reader should say so rather than report a confident zero.
func (s *Store) Exists() bool {
	_, err := os.Stat(filepath.Join(s.Dir, ".git"))
	return err == nil
}

func (s *Store) ensure(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(s.Dir, ".git")); err == nil {
		return s.configure(ctx)
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}

	if s.Remote != "" {
		if err := s.clone(ctx); err == nil {
			return s.configure(ctx)
		}
		// A remote that is unreachable or has no state branch yet is not
		// fatal; start local and publish on the next successful push.
	}
	if _, err := s.git.Run(ctx, "init", "-b", Branch, "."); err != nil {
		return fmt.Errorf("init hub: %w", err)
	}
	return s.configure(ctx)
}

func (s *Store) clone(ctx context.Context) error {
	parent := gitx.New(filepath.Dir(s.Dir))
	parent.Timeout = 2 * time.Minute
	_, err := parent.Run(ctx, "clone", "--branch", Branch, s.Remote, filepath.Base(s.Dir))
	return err
}

// configure pins an identity so commits succeed regardless of global git
// configuration, and keeps the origin URL in step with the config file.
func (s *Store) configure(ctx context.Context) error {
	for _, kv := range [][2]string{
		{"user.name", "poppy"},
		{"user.email", "poppy@localhost"},
		{"commit.gpgsign", "false"},
	} {
		if _, err := s.git.Run(ctx, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if s.Remote == "" {
		return nil
	}
	current, _ := s.git.Run(ctx, "remote", "get-url", "origin")
	switch {
	case current == "":
		_, err := s.git.Run(ctx, "remote", "add", "origin", s.Remote)
		return err
	case current != s.Remote:
		_, err := s.git.Run(ctx, "remote", "set-url", "origin", s.Remote)
		return err
	}
	return nil
}

func (s *Store) machineDir(machine string) string {
	return filepath.Join(s.Dir, "machines", machine)
}

// ReadMachine returns a machine's repositories keyed by path id, for carrying
// forward fields such as the moment a tree became dirty.
func (s *Store) ReadMachine(machine string) map[string]model.Repo {
	out := make(map[string]model.Repo)
	dir := filepath.Join(s.machineDir(machine), "repos")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var r model.Repo
		if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			if json.Unmarshal(data, &r) == nil {
				out[r.PathID] = r
			}
		}
	}
	return out
}

// Pull fetches the latest fleet state and resets the working tree to it.
// A failure means the hub is unreachable, which is reported so callers can
// mark the view as possibly stale, but is never fatal.
func (s *Store) Pull(ctx context.Context) error {
	if s.Remote == "" {
		return nil
	}
	if _, err := s.git.Run(ctx, "fetch", "--quiet", "origin", Branch); err != nil {
		return err
	}
	_, err := s.git.Run(ctx, "reset", "--hard", "--quiet", "FETCH_HEAD")
	return err
}

// Publish records this machine's state and pushes it to the hub. It returns
// whether the state reached the remote; a false return with a nil error means
// the state is committed locally and will publish on a later cycle.
func (s *Store) Publish(ctx context.Context, manifest model.Manifest, repos []model.Repo) (pushed bool, err error) {
	var lastErr error
	for range PushRetries {
		// Adopt the remote's history before writing so a push race resolves
		// by rewriting our own directory on top of whatever arrived.
		if err := s.Pull(ctx); err != nil {
			lastErr = err
		}
		if err := s.write(manifest, repos); err != nil {
			return false, err
		}
		if _, err := s.commit(ctx, manifest.Machine); err != nil {
			return false, err
		}
		if s.Remote == "" {
			return false, nil
		}
		if _, err := s.git.Run(ctx, "push", "--quiet", "origin", Branch); err == nil {
			return true, nil
		} else {
			lastErr = err
		}
	}
	return false, lastErr
}

// write replaces this machine's directory with the current scan. The
// repository directory is cleared first so repositories that have been
// deleted or moved disappear from the fleet view.
func (s *Store) write(manifest model.Manifest, repos []model.Repo) error {
	dir := s.machineDir(manifest.Machine)
	reposDir := filepath.Join(dir, "repos")
	if err := os.RemoveAll(reposDir); err != nil {
		return err
	}
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return err
	}
	for _, r := range repos {
		if err := writeJSON(filepath.Join(reposDir, r.PathID+".json"), r); err != nil {
			return err
		}
	}
	return nil
}

// commit stages this machine's directory and reports whether anything changed.
func (s *Store) commit(ctx context.Context, machine string) (bool, error) {
	pathspec := filepath.Join("machines", machine)
	if _, err := s.git.Run(ctx, "add", "-A", "--", pathspec); err != nil {
		return false, err
	}
	if out, err := s.git.Run(ctx, "status", "--porcelain", "--", pathspec); err == nil && out == "" {
		return false, nil
	}
	msg := fmt.Sprintf("state(%s): %s", machine, time.Now().UTC().Format(time.RFC3339))
	if _, err := s.git.Run(ctx, "commit", "--quiet", "-m", msg, "--", pathspec); err != nil {
		return false, err
	}
	return true, nil
}

// Fleet reads every machine's state from the working tree.
func (s *Store) Fleet() (model.Fleet, error) {
	var fleet model.Fleet
	root := filepath.Join(s.Dir, "machines")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return fleet, nil
	}
	if err != nil {
		return fleet, err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ms := model.MachineState{}
		manifestPath := filepath.Join(root, e.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &ms.Manifest); err != nil {
			continue
		}
		for _, r := range s.ReadMachine(e.Name()) {
			ms.Repos = append(ms.Repos, r)
		}
		sort.Slice(ms.Repos, func(i, j int) bool { return ms.Repos[i].Path < ms.Repos[j].Path })
		fleet.Machines = append(fleet.Machines, ms)
	}
	sort.Slice(fleet.Machines, func(i, j int) bool {
		return fleet.Machines[i].Manifest.Machine < fleet.Machines[j].Manifest.Machine
	})
	return fleet, nil
}

// ReadManifest returns a machine's last published manifest, for carrying
// forward what the previous cycle recorded. The second return distinguishes a
// machine that has never published from one whose manifest is all zeroes.
func (s *Store) ReadManifest(machine string) (model.Manifest, bool) {
	data, err := os.ReadFile(filepath.Join(s.machineDir(machine), "manifest.json"))
	if err != nil {
		return model.Manifest{}, false
	}
	var m model.Manifest
	if json.Unmarshal(data, &m) != nil {
		return model.Manifest{}, false
	}
	return m, true
}

// NextSeq returns the machine's next monotonic sequence number. Sequence
// numbers order a machine's updates without trusting wall clocks.
func (s *Store) NextSeq(machine string) uint64 {
	m, ok := s.ReadManifest(machine)
	if !ok {
		return 1
	}
	return m.Seq + 1
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
