// Package mirror keeps each repository's snapshots on a bare repository on the
// closet server, one per project.
//
// A per-repository mirror means the first push of a project transfers its
// history once and later pushes send only deltas — unlike a single shared hub,
// which would hold none of the base objects and re-transfer everything. The
// mirror doubles as an off-machine backup: because a snapshot commit's parents
// include HEAD, pushing only the poppy refs carries the whole object closure,
// so a machine that has never seen a project can still fetch and apply another
// machine's uncommitted work from it.
//
// Mirrors are keyed by repoId, the identity derived from the canonical remote
// URL, so every machine working on one project shares its mirror. Poppy refs
// are already namespaced by machine, so those shared pushes never collide.
package mirror

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mads/poppy/internal/gitx"
	"github.com/mads/poppy/internal/snapshot"
)

// Config locates the mirrors.
type Config struct {
	// Host is the SSH target of the closet server, for example
	// "devbox.tailnet.ts.net" or "mads@devbox". When empty, Dir is treated as
	// a path on the local machine, which suits a single-box setup and tests.
	Host string
	// Dir is the directory holding the bare mirrors, on Host when set.
	Dir string
}

// Enabled reports whether a mirror destination is configured.
func (c Config) Enabled() bool { return c.Dir != "" }

// Mirror pushes to and fetches from one project's bare repository.
type Mirror struct {
	cfg     Config
	repoID  string
	timeout time.Duration
}

// For returns the mirror for a repository identity.
func For(cfg Config, repoID string) *Mirror {
	return &Mirror{cfg: cfg, repoID: repoID, timeout: 2 * time.Minute}
}

// relPath is the mirror's path beneath the configured directory.
func (m *Mirror) relPath() string { return path.Join(m.cfg.Dir, m.repoID+".git") }

// url is the git remote address of the mirror.
func (m *Mirror) url() string {
	if m.cfg.Host == "" {
		return m.relPath()
	}
	// scp-style host:path, which git understands directly.
	return m.cfg.Host + ":" + m.relPath()
}

// Ensure creates the bare mirror when it does not yet exist. git init on an
// existing bare repository is harmless, so concurrent creation by two machines
// is safe.
func (m *Mirror) Ensure(ctx context.Context) error {
	if m.cfg.Host == "" {
		if err := os.MkdirAll(m.relPath(), 0o755); err != nil {
			return err
		}
		_, err := gitx.New(m.relPath()).Run(ctx, "init", "--bare", "--quiet", ".")
		return err
	}

	remote := fmt.Sprintf("mkdir -p %s && git init --bare --quiet %s",
		shellQuote(m.cfg.Dir), shellQuote(m.relPath()))
	return m.ssh(ctx, remote)
}

// machineRefspecs are the wip and snap namespaces for one machine. A refspec
// permits only one wildcard, so the two ref roots need one refspec each; the
// wildcard matches the branch and timestamp components, slashes included.
func machineRefspecs(machine string) []string {
	return []string{
		fmt.Sprintf("refs/poppy/wip/%s/*:refs/poppy/wip/%s/*", machine, machine),
		fmt.Sprintf("refs/poppy/snap/%s/*:refs/poppy/snap/%s/*", machine, machine),
	}
}

// Push sends this machine's snapshot refs to the mirror, creating it first if
// needed. Only refs/poppy/* is pushed; the base objects follow because they
// are ancestors of the snapshot commits.
func (m *Mirror) Push(ctx context.Context, repoPath, machine string) error {
	if err := m.Ensure(ctx); err != nil {
		return fmt.Errorf("prepare mirror: %w", err)
	}
	g := m.git(repoPath)
	args := append([]string{"push", "--force", "--prune", m.url()}, machineRefspecs(machine)...)
	if _, err := g.Run(ctx, args...); err != nil {
		return fmt.Errorf("push snapshots: %w", err)
	}
	return nil
}

// FetchMachine retrieves another machine's snapshot refs from the mirror into
// the local repository. Only the named machine's refs are fetched, so this
// machine's own snapshots are never disturbed.
func (m *Mirror) FetchMachine(ctx context.Context, repoPath, machine string) error {
	g := m.git(repoPath)
	args := append([]string{"fetch", "--force", "--prune", m.url()}, machineRefspecs(machine)...)
	if _, err := g.Run(ctx, args...); err != nil {
		return fmt.Errorf("fetch snapshots for %s: %w", machine, err)
	}
	return nil
}

// FetchInto clones enough of the mirror to resolve a machine's snapshots into
// a repository that may not yet share the project's history. It is used by grab
// on a machine that has the project checked out but is missing the source
// machine's objects.
func (m *Mirror) FetchInto(ctx context.Context, repoPath, machine string) (snapshot.Snapshot, bool, error) {
	if err := m.FetchMachine(ctx, repoPath, machine); err != nil {
		return snapshot.Snapshot{}, false, err
	}
	snaps, err := snapshot.List(ctx, repoPath, machine, "")
	if err != nil {
		return snapshot.Snapshot{}, false, err
	}
	if len(snaps) == 0 {
		return snapshot.Snapshot{}, false, nil
	}
	return snaps[0], true, nil
}

func (m *Mirror) git(repoPath string) *gitx.Runner {
	g := gitx.New(repoPath)
	g.Timeout = m.timeout
	return g
}

func (m *Mirror) ssh(ctx context.Context, remoteCommand string) error {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		m.cfg.Host, remoteCommand)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("ssh %s: %w", m.cfg.Host, err)
		}
		return fmt.Errorf("ssh %s: %s", m.cfg.Host, msg)
	}
	return nil
}

// shellQuote wraps a path in single quotes for safe interpolation into the
// remote shell command, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// LocalConfigForTest builds a config backed by a local directory. It exists so
// tests exercise the real push and fetch paths without an SSH server.
func LocalConfigForTest(dir string) Config { return Config{Dir: filepath.Clean(dir)} }
