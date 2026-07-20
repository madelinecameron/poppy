// Package collect gathers the state of a single repository.
package collect

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mads/poppy/internal/gitx"
	"github.com/mads/poppy/internal/identity"
	"github.com/mads/poppy/internal/model"
)

// Collector reads repository state for one machine.
type Collector struct {
	Machine string
	Now     func() time.Time
}

// New returns a Collector for the named machine.
func New(machine string) *Collector {
	return &Collector{Machine: machine, Now: time.Now}
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Repo collects state for the repository at path. prev is that repository's
// previously recorded state, used to carry forward the moment a working tree
// first became dirty; it may be nil.
//
// Failures of individual git commands degrade the document rather than
// failing it: a repository that cannot be fully read is still worth
// reporting, and the reason is recorded in Error.
func (c *Collector) Repo(ctx context.Context, path string, isWorktree bool, prev *model.Repo) model.Repo {
	g := gitx.New(path)
	now := c.now()

	top, topErr := g.Run(ctx, "rev-parse", "--show-toplevel")
	if topErr == nil && top != "" {
		path = top
	}

	r := model.Repo{
		Schema:     model.SchemaVersion,
		Machine:    c.Machine,
		Name:       filepath.Base(path),
		Path:       path,
		PathID:     identity.PathID(c.Machine, path),
		IsWorktree: isWorktree,
		UpdatedAt:  now,
	}

	// A directory git refuses to open will fail every subsequent command with
	// the same error, so report the cause once rather than running them.
	if topErr != nil {
		r.Broken = true
		r.RepoID = identity.RepoID("", path)
		r.Error = brokenReason(path, topErr)
		return r
	}

	var problems []string
	fail := func(what string, err error) {
		if err != nil {
			problems = append(problems, what+": "+err.Error())
		}
	}

	remotes, err := c.remotes(ctx, g)
	fail("remotes", err)
	r.Remotes = remotes
	r.RepoID = identity.RepoID(primaryRemote(remotes), path)

	st, err := g.Status(ctx)
	fail("status", err)
	r.Head = st.Head
	r.Dirty = st.Dirty

	branches, err := c.branches(ctx, g)
	fail("branches", err)
	r.Branches = branches

	if st.Upstream != "" && r.Head.Branch != "" {
		for i := range r.Branches {
			if r.Branches[i].Name == r.Head.Branch {
				r.Branches[i].Upstream = st.Upstream
				r.Branches[i].Ahead = st.Ahead
				r.Branches[i].Behind = st.Behind
			}
		}
	}

	stashes, err := c.stashes(ctx, g)
	fail("stash", err)
	r.Stashes = stashes

	if !isWorktree {
		worktrees, err := c.worktrees(ctx, g, path)
		fail("worktree", err)
		r.Worktrees = worktrees
	}

	if !r.Head.Unborn {
		commit, err := c.lastCommit(ctx, g)
		fail("log", err)
		r.LastCommit = commit
	}

	r.LastFetch = c.lastFetch(ctx, g)
	r.DirtySince = dirtySince(r.Dirty, prev, now)
	r.Error = strings.Join(problems, "; ")

	return r
}

// brokenReason explains, in one line, why a directory that looks like a
// working tree cannot be read as one. The common case is a worktree left
// behind after its parent repository was removed or pruned: git can no longer
// see the directory at all, so anything in it is invisible to every ordinary
// git command.
func brokenReason(path string, err error) string {
	if target, ok := worktreeGitDir(path); ok {
		if _, statErr := os.Stat(target); statErr != nil {
			return "orphaned worktree: " + target + " is missing"
		}
	}
	return firstLine(err.Error())
}

// worktreeGitDir reads the gitdir pointer from a .git file, which a linked
// worktree uses in place of a .git directory.
func worktreeGitDir(path string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", false
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return "", false
	}
	return target, true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// dirtySince preserves the original transition to dirty across scans so the
// age of stranded work is measured from when it appeared, not when it was
// last observed.
func dirtySince(d model.Dirty, prev *model.Repo, now time.Time) *time.Time {
	if !d.Any() {
		return nil
	}
	if prev != nil && prev.DirtySince != nil && prev.Dirty.Any() {
		return prev.DirtySince
	}
	return &now
}

var remoteURLKey = regexp.MustCompile(`^remote\.(.+)\.url$`)

func (c *Collector) remotes(ctx context.Context, g *gitx.Runner) (map[string]string, error) {
	lines, err := g.Lines(ctx, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		// A repository with no remotes exits non-zero here; that is not a fault.
		return nil, nil
	}
	remotes := make(map[string]string, len(lines))
	for _, line := range lines {
		key, url, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if m := remoteURLKey.FindStringSubmatch(key); m != nil {
			remotes[m[1]] = url
		}
	}
	return remotes, nil
}

// primaryRemote prefers origin, then upstream, then any remote by name order,
// so repo identity stays stable as remotes are added.
func primaryRemote(remotes map[string]string) string {
	for _, name := range []string{"origin", "upstream"} {
		if url, ok := remotes[name]; ok {
			return url
		}
	}
	best := ""
	bestName := ""
	for name, url := range remotes {
		if bestName == "" || name < bestName {
			bestName, best = name, url
		}
	}
	return best
}

var trackRE = regexp.MustCompile(`(ahead|behind) (\d+)`)

func (c *Collector) branches(ctx context.Context, g *gitx.Runner) ([]model.Branch, error) {
	const format = "%(refname:short)%00%(objectname)%00%(upstream:short)%00%(upstream:track)%00"
	fields, err := g.RunZ(ctx, "for-each-ref", "--format="+format, "refs/heads")
	if err != nil {
		return nil, err
	}

	var branches []model.Branch
	// for-each-ref has no -z mode and terminates every record with a newline,
	// so each record after the first carries a leading newline on its name.
	// Ref names may not contain whitespace, making the trim unambiguous.
	for i := 0; i+3 < len(fields); i += 4 {
		b := model.Branch{
			Name:     strings.TrimSpace(fields[i]),
			SHA:      fields[i+1],
			Upstream: fields[i+2],
		}
		track := fields[i+3]
		if strings.Contains(track, "gone") {
			b.Gone = true
		}
		for _, m := range trackRE.FindAllStringSubmatch(track, -1) {
			n, _ := strconv.Atoi(m[2])
			if m[1] == "ahead" {
				b.Ahead = n
			} else {
				b.Behind = n
			}
		}
		branches = append(branches, b)
	}
	return branches, nil
}

func (c *Collector) stashes(ctx context.Context, g *gitx.Runner) (int, error) {
	out, err := g.Run(ctx, "rev-list", "--walk-reflogs", "--count", "refs/stash")
	if err != nil {
		// No stash ref exists; an empty stash is not an error.
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func (c *Collector) worktrees(ctx context.Context, g *gitx.Runner, main string) ([]string, error) {
	lines, err := g.Lines(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range lines {
		path, ok := strings.CutPrefix(line, "worktree ")
		if ok && path != main {
			out = append(out, path)
		}
	}
	return out, nil
}

func (c *Collector) lastCommit(ctx context.Context, g *gitx.Runner) (*model.Commit, error) {
	fields, err := g.RunZ(ctx, "log", "-1", "--format=%H%x00%s%x00%an%x00%aI")
	if err != nil || len(fields) < 4 {
		return nil, err
	}
	// git log appends a newline after the format, which lands on the final
	// NUL-delimited field. Left in place it makes the timestamp fail to parse,
	// silently zeroing every commit date.
	when, _ := time.Parse(time.RFC3339, strings.TrimSpace(fields[3]))
	return &model.Commit{
		SHA:     fields[0],
		Subject: fields[1],
		Author:  fields[2],
		When:    when,
	}, nil
}

// lastFetch reads the mtime of FETCH_HEAD, which git touches on every fetch.
func (c *Collector) lastFetch(ctx context.Context, g *gitx.Runner) *time.Time {
	gitDir, err := g.Run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil
	}
	info, err := os.Stat(filepath.Join(gitDir, "FETCH_HEAD"))
	if err != nil {
		return nil
	}
	t := info.ModTime()
	return &t
}
