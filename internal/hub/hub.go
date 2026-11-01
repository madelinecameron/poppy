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
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/deploys"
	"github.com/madelinecameron/poppy/internal/gitx"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/sessions"
)

// Branch is the branch holding state documents.
const Branch = "state"

// PushRetries bounds how many times a push race is retried.
const PushRetries = 3

// Store is a local clone of the hub repository, or a read-only view of a
// clone another poppy keeps (see OpenCopy).
type Store struct {
	Dir    string
	Remote string
	git    *gitx.Runner
	copy   *copyView
}

// files is the tree reads see: the working tree of this machine's own clone,
// or the last commit of a copy taken whole, never a tree half way through a
// reset.
func (s *Store) files() fs.FS {
	if s.copy != nil {
		return s.copy.files
	}
	return os.DirFS(s.Dir)
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
	if s.copy != nil {
		return true
	}
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
	dir := path.Join("machines", machine, "repos")
	entries, err := fs.ReadDir(s.files(), dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var r model.Repo
		if data, err := fs.ReadFile(s.files(), path.Join(dir, e.Name())); err == nil {
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
	if s.copy != nil {
		return s.copy.refresh(ctx)
	}
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
	return s.publish(ctx, manifest.Machine, true, stateMessage(manifest.Machine), func() error { return s.write(manifest, repos) })
}

// PublishSessions replaces this machine's harness sessions in the hub. It is
// separate from Publish so a session that starts or changes branch can reach
// the fleet without waiting for a full scan. Nothing is pushed when the
// sessions are unchanged, which is the usual case for a scheduled call.
func (s *Store) PublishSessions(ctx context.Context, machine string, records []model.Session) (pushed bool, err error) {
	return s.publish(ctx, machine, false, stateMessage(machine), func() error { return s.writeSessions(machine, records) })
}

// PublishDeploys replaces this machine's deploys in the hub. Like sessions,
// nothing is pushed when they are unchanged.
func (s *Store) PublishDeploys(ctx context.Context, machine string, records []model.Deploy) (pushed bool, err error) {
	return s.publish(ctx, machine, false, stateMessage(machine), func() error { return s.writeDeploys(machine, records) })
}

// ValidMachine reports whether name can only ever name one directory under
// machines/. A name that could reach outside it is never used as a path.
func ValidMachine(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}

// MachineFiles lists the files a machine has in the hub, relative to the hub
// root, sorted. A machine that never published has none.
func (s *Store) MachineFiles(machine string) ([]string, error) {
	if !ValidMachine(machine) {
		return nil, fmt.Errorf("invalid machine name %q", machine)
	}
	var out []string
	err := fs.WalkDir(s.files(), path.Join("machines", machine), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	sort.Strings(out)
	return out, err
}

// Retire removes every record a machine published, in one commit, and pushes
// it. It is for a machine that is gone for good: one that is still running
// poppy writes its directory back on its next scan.
func (s *Store) Retire(ctx context.Context, machine string) (pushed bool, err error) {
	if !ValidMachine(machine) {
		return false, fmt.Errorf("invalid machine name %q", machine)
	}
	msg := fmt.Sprintf("retire(%s): %s", machine, time.Now().UTC().Format(time.RFC3339))
	return s.publish(ctx, machine, false, msg, func() error { return os.RemoveAll(s.machineDir(machine)) })
}

func stateMessage(machine string) string {
	return fmt.Sprintf("state(%s): %s", machine, time.Now().UTC().Format(time.RFC3339))
}

// publish runs one write-commit-push cycle on this machine's directory. With
// always false, a write that changes nothing pushes nothing.
func (s *Store) publish(ctx context.Context, machine string, always bool, msg string, write func() error) (bool, error) {
	if s.copy != nil {
		return false, errReadOnlyCopy
	}
	var lastErr error
	for range PushRetries {
		// Adopt the remote's history before writing so a push race resolves
		// by rewriting our own directory on top of whatever arrived.
		if err := s.Pull(ctx); err != nil {
			lastErr = err
		}
		if err := write(); err != nil {
			return false, err
		}
		changed, err := s.commit(ctx, machine, msg)
		if err != nil {
			return false, err
		}
		if s.Remote == "" {
			return false, nil
		}
		if !changed && !always && lastErr == nil {
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

// writeSessions replaces this machine's sessions directory, so a session
// pruned locally disappears from the fleet view too.
func (s *Store) writeSessions(machine string, records []model.Session) error {
	dir := filepath.Join(s.machineDir(machine), "sessions")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, sess := range records {
		if err := writeJSON(filepath.Join(dir, sessions.FileName(sess.SessionID)), sess); err != nil {
			return err
		}
	}
	return nil
}

// writeDeploys replaces this machine's deploys directory, so a deploy pruned
// locally disappears from the hub too.
func (s *Store) writeDeploys(machine string, records []model.Deploy) error {
	dir := filepath.Join(s.machineDir(machine), "deploys")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for _, d := range records {
		if err := writeJSON(filepath.Join(dir, deploys.FileName(d.ID)), d); err != nil {
			return err
		}
	}
	return nil
}

// ReadDeploys returns a machine's published deploys, newest first.
func (s *Store) ReadDeploys(machine string) []model.Deploy {
	dir := path.Join("machines", machine, "deploys")
	entries, err := fs.ReadDir(s.files(), dir)
	if err != nil {
		return nil
	}
	var out []model.Deploy
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(s.files(), path.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if d, err := deploys.Parse(data); err == nil {
			if d.Machine == "" {
				d.Machine = machine
			}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// ReadSessions returns a machine's published harness sessions.
func (s *Store) ReadSessions(machine string) []model.Session {
	dir := path.Join("machines", machine, "sessions")
	entries, err := fs.ReadDir(s.files(), dir)
	if err != nil {
		return nil
	}
	var out []model.Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var sess model.Session
		if data, err := fs.ReadFile(s.files(), path.Join(dir, e.Name())); err == nil {
			if json.Unmarshal(data, &sess) == nil && sess.SessionID != "" {
				if sess.Machine == "" {
					sess.Machine = machine
				}
				out = append(out, sess)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
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
func (s *Store) commit(ctx context.Context, machine, msg string) (bool, error) {
	pathspec := filepath.Join("machines", machine)
	if _, err := s.git.Run(ctx, "add", "-A", "--", pathspec); err != nil {
		return false, err
	}
	if out, err := s.git.Run(ctx, "status", "--porcelain", "--", pathspec); err == nil && out == "" {
		return false, nil
	}
	if _, err := s.git.Run(ctx, "commit", "--quiet", "-m", msg, "--", pathspec); err != nil {
		return false, err
	}
	return true, nil
}

// Fleet reads every machine's state.
func (s *Store) Fleet() (model.Fleet, error) {
	var fleet model.Fleet
	entries, err := fs.ReadDir(s.files(), "machines")
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
		data, err := fs.ReadFile(s.files(), path.Join("machines", e.Name(), "manifest.json"))
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
		ms.Sessions = s.ReadSessions(e.Name())
		ms.Deploys = s.ReadDeploys(e.Name())
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
	data, err := fs.ReadFile(s.files(), path.Join("machines", machine, "manifest.json"))
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
