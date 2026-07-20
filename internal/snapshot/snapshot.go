// Package snapshot captures uncommitted work as git commits that live outside
// the branch graph.
//
// A snapshot uses git's own stash commit shape, so the staged, unstaged, and
// untracked parts of a working tree survive intact and `git stash apply`
// restores one without poppy's help:
//
//	parent 1  HEAD at capture time
//	parent 2  the index
//	parent 3  untracked files (omitted when there are none)
//	tree      the working tree
//
// Nothing here writes to HEAD, the index, or the working tree. Trees are built
// through a scratch index copied from the real one, so a capture can never
// disturb work in progress or contend for the index lock.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mads/poppy/internal/gitx"
	"github.com/mads/poppy/internal/model"
)

// DefaultMaxFileBytes bounds the size of an untracked file poppy will capture.
// Untracked files are frequently build output or downloaded artifacts that a
// gitignore has not caught yet, and copying those into the object store would
// bloat the repository for no benefit. Skipped paths are always reported.
const DefaultMaxFileBytes = 10 << 20

// Snapshot describes one captured working tree.
type Snapshot struct {
	SHA     string    `json:"sha"`
	Machine string    `json:"machine"`
	Branch  string    `json:"branch"`
	Taken   time.Time `json:"taken"`
	Ref     string    `json:"ref"`
	Files   int       `json:"files"`
	Skipped []string  `json:"skipped,omitempty"`
}

// Snapshotter captures snapshots for one machine.
type Snapshotter struct {
	Machine      string
	MaxFileBytes int64
	Now          func() time.Time
}

// New returns a Snapshotter for the named machine.
func New(machine string) *Snapshotter {
	return &Snapshotter{Machine: machine, MaxFileBytes: DefaultMaxFileBytes, Now: time.Now}
}

func (s *Snapshotter) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Snapshotter) maxBytes() int64 {
	if s.MaxFileBytes > 0 {
		return s.MaxFileBytes
	}
	return DefaultMaxFileBytes
}

// ErrNothingToCapture reports a working tree that matches HEAD.
var ErrNothingToCapture = errors.New("working tree is clean")

// ErrUnchanged reports a working tree identical to the previous snapshot.
var ErrUnchanged = errors.New("working tree unchanged since last snapshot")

// Create captures the working tree at repoPath. It returns ErrNothingToCapture
// when the tree is clean and ErrUnchanged when nothing has moved since the
// last capture, so repeated scans do not accumulate identical snapshots.
func (s *Snapshotter) Create(ctx context.Context, repoPath string) (*Snapshot, error) {
	g := gitx.New(repoPath)

	status, err := g.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read status: %w", err)
	}
	if !status.Dirty.Any() {
		return nil, ErrNothingToCapture
	}

	branch := branchLabel(status.Head)

	scratch, err := os.MkdirTemp("", "poppy-snapshot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)

	trees, err := s.buildTrees(ctx, g, repoPath, scratch, status.Head.Unborn)
	if err != nil {
		return nil, err
	}

	previous, _ := g.Run(ctx, "rev-parse", "--verify", "--quiet", LatestRef(s.Machine, branch))
	if previous != "" && s.matchesPrevious(ctx, g, previous, trees) {
		return nil, ErrUnchanged
	}

	ref, taken := freeHistoryRef(ctx, g, s.Machine, branch, s.now())
	sha, err := s.commit(ctx, g, trees, branch, taken, status.Head.Unborn)
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		SHA:     sha,
		Machine: s.Machine,
		Branch:  branch,
		Taken:   taken,
		Ref:     ref,
		Files:   status.Dirty.Total(),
		Skipped: trees.skipped,
	}

	if _, err := g.Run(ctx, "update-ref", snap.Ref, sha); err != nil {
		return nil, fmt.Errorf("record snapshot: %w", err)
	}
	if _, err := g.Run(ctx, "update-ref", LatestRef(s.Machine, branch), sha); err != nil {
		return nil, fmt.Errorf("advance latest ref: %w", err)
	}
	return snap, nil
}

// freeHistoryRef finds an unused history ref at or just after a moment, so two
// captures in the same millisecond are both recorded rather than one replacing
// the other.
func freeHistoryRef(ctx context.Context, g *gitx.Runner, machine, branch string, taken time.Time) (string, time.Time) {
	const maxProbe = 1000
	for i := range maxProbe {
		candidate := taken.Add(time.Duration(i) * time.Millisecond)
		ref := HistoryRef(machine, branch, candidate)
		if existing, _ := g.Run(ctx, "rev-parse", "--verify", "--quiet", ref); existing == "" {
			return ref, candidate
		}
	}
	return HistoryRef(machine, branch, taken), taken
}

type trees struct {
	work      string
	index     string
	untracked string
	skipped   []string
}

// buildTrees writes the three trees a stash commit refers to, using a scratch
// index so the repository's own index is never read under a lock or modified.
func (s *Snapshotter) buildTrees(ctx context.Context, g *gitx.Runner, repoPath, scratch string, unborn bool) (trees, error) {
	var t trees

	// Untracked paths must be listed against the real index. Against an empty
	// scratch index every file looks untracked, which would sweep the entire
	// repository into the untracked tree.
	untracked, err := g.RunZ(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return t, fmt.Errorf("list untracked: %w", err)
	}
	keep, skipped := s.filterBySize(repoPath, untracked)
	t.skipped = skipped

	indexPath := filepath.Join(scratch, "index")
	if !unborn {
		if err := copyFile(filepath.Join(gitDir(ctx, g), "index"), indexPath); err != nil {
			return t, fmt.Errorf("copy index: %w", err)
		}
	}
	scratchIndex := g.With("GIT_INDEX_FILE=" + indexPath)

	if t.index, err = scratchIndex.Run(ctx, "write-tree"); err != nil {
		return t, fmt.Errorf("write index tree: %w", err)
	}
	if _, err := scratchIndex.Run(ctx, "add", "--update"); err != nil {
		return t, fmt.Errorf("stage working tree: %w", err)
	}
	if t.work, err = scratchIndex.Run(ctx, "write-tree"); err != nil {
		return t, fmt.Errorf("write working tree: %w", err)
	}

	if len(keep) > 0 {
		untrackedIndex := g.With("GIT_INDEX_FILE=" + filepath.Join(scratch, "untracked-index"))
		paths := strings.Join(keep, "\x00") + "\x00"
		if _, err := untrackedIndex.WithStdin([]byte(paths)).Run(ctx, "update-index", "--add", "-z", "--stdin"); err != nil {
			return t, fmt.Errorf("stage untracked files: %w", err)
		}
		if t.untracked, err = untrackedIndex.Run(ctx, "write-tree"); err != nil {
			return t, fmt.Errorf("write untracked tree: %w", err)
		}
	}
	return t, nil
}

// filterBySize splits untracked paths into those small enough to capture and
// those deliberately left behind.
func (s *Snapshotter) filterBySize(repoPath string, paths []string) (keep, skipped []string) {
	limit := s.maxBytes()
	for _, p := range paths {
		if p == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(repoPath, p))
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.Size() > limit {
			skipped = append(skipped, p)
			continue
		}
		keep = append(keep, p)
	}
	return keep, skipped
}

// matchesPrevious reports whether a snapshot would duplicate the last one.
func (s *Snapshotter) matchesPrevious(ctx context.Context, g *gitx.Runner, previous string, t trees) bool {
	tree := func(rev string) string {
		out, err := g.Run(ctx, "rev-parse", "--verify", "--quiet", rev+"^{tree}")
		if err != nil {
			return ""
		}
		return out
	}
	return tree(previous) == t.work &&
		tree(previous+"^2") == t.index &&
		tree(previous+"^3") == t.untracked
}

func (s *Snapshotter) commit(ctx context.Context, g *gitx.Runner, t trees, branch string, taken time.Time, unborn bool) (string, error) {
	stamp := taken.UTC().Format(time.RFC3339)

	// git records author and committer dates from the environment, which keeps
	// snapshot commits reproducible and stops them from inheriting a stale
	// timestamp on a machine with an unusual clock.
	dated := g.With(
		"GIT_AUTHOR_DATE="+stamp,
		"GIT_COMMITTER_DATE="+stamp,
		"GIT_AUTHOR_NAME=poppy",
		"GIT_AUTHOR_EMAIL=poppy@"+s.Machine,
		"GIT_COMMITTER_NAME=poppy",
		"GIT_COMMITTER_EMAIL=poppy@"+s.Machine,
	)

	var head string
	if !unborn {
		var err error
		if head, err = g.Run(ctx, "rev-parse", "HEAD"); err != nil {
			return "", fmt.Errorf("resolve HEAD: %w", err)
		}
	}

	indexArgs := []string{"commit-tree", t.index}
	if head != "" {
		indexArgs = append(indexArgs, "-p", head)
	}
	indexArgs = append(indexArgs, "-m", "index on "+branch)
	indexCommit, err := dated.Run(ctx, indexArgs...)
	if err != nil {
		return "", fmt.Errorf("commit index tree: %w", err)
	}

	args := []string{"commit-tree", t.work}
	if head != "" {
		args = append(args, "-p", head)
	}
	args = append(args, "-p", indexCommit)

	if t.untracked != "" {
		untrackedCommit, err := dated.Run(ctx, "commit-tree", t.untracked, "-m", "untracked files on "+branch)
		if err != nil {
			return "", fmt.Errorf("commit untracked tree: %w", err)
		}
		args = append(args, "-p", untrackedCommit)
	}

	args = append(args, "-m", fmt.Sprintf("poppy snapshot on %s at %s", branch, stamp))
	sha, err := dated.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("commit working tree: %w", err)
	}
	return sha, nil
}

// List returns the snapshots held in a repository, newest first. An empty
// machine or branch matches all of them.
func List(ctx context.Context, repoPath, machine, branch string) ([]Snapshot, error) {
	g := gitx.New(repoPath)
	lines, err := g.Lines(ctx, "for-each-ref", "--format=%(refname)%00%(objectname)", HistoryPrefix)
	if err != nil {
		return nil, err
	}

	var out []Snapshot
	for _, line := range lines {
		ref, sha, ok := strings.Cut(strings.TrimSpace(line), "\x00")
		if !ok {
			continue
		}
		refMachine, refBranch, taken, ok := parseHistoryRef(ref)
		if !ok {
			continue
		}
		if machine != "" && refMachine != machine {
			continue
		}
		if branch != "" && refBranch != branch {
			continue
		}
		out = append(out, Snapshot{
			SHA: sha, Machine: refMachine, Branch: refBranch, Taken: taken, Ref: ref,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Taken.After(out[j].Taken) })
	return out, nil
}

// Latest returns the most recent snapshot of a branch, read from the wip ref
// that Create advances. It reports false when the branch has none.
func Latest(ctx context.Context, repoPath, machine, branch string) (Snapshot, bool) {
	g := gitx.New(repoPath)
	ref := LatestRef(machine, branch)
	sha, err := g.Run(ctx, "rev-parse", "--verify", "--quiet", ref)
	if err != nil || sha == "" {
		return Snapshot{}, false
	}

	taken := time.Time{}
	if when, err := g.Run(ctx, "log", "-1", "--format=%cI", sha); err == nil {
		if parsed, err := time.Parse(time.RFC3339, when); err == nil {
			taken = parsed
		}
	}
	return Snapshot{SHA: sha, Machine: machine, Branch: branch, Taken: taken, Ref: ref}, true
}

// Policy decides which snapshots survive a prune.
type Policy struct {
	// KeepAll retains every snapshot taken within this window.
	KeepAll time.Duration
	// KeepDaily retains the newest snapshot of each day within this window.
	KeepDaily time.Duration
}

// DefaultPolicy keeps everything for a week and one a day for a quarter.
func DefaultPolicy() Policy {
	return Policy{KeepAll: 7 * 24 * time.Hour, KeepDaily: 90 * 24 * time.Hour}
}

// Retain reports which snapshots a policy keeps, given newest-first input.
// The newest snapshot of a branch is always retained: it is the live record of
// current work, however old the machine's last scan may be.
func (p Policy) Retain(snaps []Snapshot, now time.Time) (keep, drop []Snapshot) {
	newestPerBranch := make(map[string]bool)
	keptDay := make(map[string]bool)

	for _, s := range snaps {
		age := now.Sub(s.Taken)
		branchKey := s.Machine + "/" + s.Branch
		dayKey := branchKey + "/" + s.Taken.Format(time.DateOnly)

		var retain bool
		switch {
		case !newestPerBranch[branchKey]:
			newestPerBranch[branchKey] = true
			retain = true
		case age <= p.KeepAll:
			retain = true
		case age <= p.KeepDaily:
			retain = !keptDay[dayKey]
		}

		if !retain {
			drop = append(drop, s)
			continue
		}
		// Every retained snapshot claims its day, including the branch's
		// newest. Otherwise a second snapshot from the same day would find the
		// day unclaimed and survive a thinning that should have removed it.
		keptDay[dayKey] = true
		keep = append(keep, s)
	}
	return keep, drop
}

// Prune deletes snapshots a policy no longer keeps and returns how many went.
func Prune(ctx context.Context, repoPath, machine string, p Policy, now time.Time) (int, error) {
	snaps, err := List(ctx, repoPath, machine, "")
	if err != nil {
		return 0, err
	}
	_, drop := p.Retain(snaps, now)
	if len(drop) == 0 {
		return 0, nil
	}

	g := gitx.New(repoPath)
	var commands strings.Builder
	for _, s := range drop {
		fmt.Fprintf(&commands, "delete %s\x00%s\x00", s.Ref, s.SHA)
	}
	if _, err := g.WithStdin([]byte(commands.String())).Run(ctx, "update-ref", "-z", "--stdin"); err != nil {
		return 0, fmt.Errorf("delete snapshot refs: %w", err)
	}
	return len(drop), nil
}

// Restore applies a snapshot to the working tree at repoPath.
//
// The snapshot's stash shape means git itself performs the restore, rebuilding
// the staged, unstaged, and untracked split exactly as captured. A tree with
// its own uncommitted changes is refused, because applying over it would merge
// two sets of work with no way to tell them apart afterwards.
func Restore(ctx context.Context, repoPath, sha string, force bool) error {
	g := gitx.New(repoPath)

	if !force {
		status, err := g.Status(ctx)
		if err != nil {
			return err
		}
		if status.Dirty.Any() {
			return fmt.Errorf("working tree has %d uncommitted change(s); commit, stash, or pass --force",
				status.Dirty.Total())
		}
	}
	if _, err := g.Run(ctx, "stash", "apply", sha); err != nil {
		return fmt.Errorf("apply snapshot: %w", err)
	}
	return nil
}

// Resolve finds the snapshot in effect at a moment, which is the newest one
// taken at or before it.
func Resolve(snaps []Snapshot, at time.Time) (Snapshot, bool) {
	for _, s := range snaps {
		if !s.Taken.After(at) {
			return s, true
		}
	}
	return Snapshot{}, false
}

func branchLabel(head model.Head) string {
	switch {
	case head.Branch != "":
		return head.Branch
	case head.Detached:
		return "detached"
	}
	return "unborn"
}

func gitDir(ctx context.Context, g *gitx.Runner) string {
	dir, err := g.Run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return ""
	}
	return dir
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		// A repository whose index has not been written yet is legitimate;
		// an absent scratch index simply starts empty.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// FormatAge renders how long ago a snapshot was taken.
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}
