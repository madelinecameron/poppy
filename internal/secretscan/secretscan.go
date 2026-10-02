// Package secretscan scans a repository for credentials, including the
// content that exists in exactly one place.
//
// A pre-commit hook can only see the commit being made. It does nothing about
// the credential that was committed last March, and nothing at all about the
// two places git keeps content that no remote has ever seen: stashes, and
// commits on local branches that are not reachable from any remote-tracking
// branch. Those are invisible from every other machine and are lost with the
// disk they sit on, which is exactly the class of work poppy exists to find.
package secretscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/gitx"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/secrets"
)

// Defaults for the bounds that keep a scan usable on a large repository.
// Every one of them is reported when it bites: a quiet report has to mean a
// clean repository, not an exhausted budget.
const (
	DefaultMaxFileBytes       = 512 << 10
	DefaultMaxWorktreeFiles   = 20000
	DefaultMaxUnpushedCommits = 500
	DefaultMaxBlobs           = 5000
	DefaultMaxFindings        = 200
	DefaultTimeout            = 60 * time.Second
)

// blobBatch is how many objects are read from git at a time. Reading them all
// at once would hold the whole set in memory; one cat-file per object would
// pay process startup thousands of times.
const blobBatch = 64

// Options bounds a scan.
type Options struct {
	MaxFileBytes       int64
	MaxWorktreeFiles   int
	MaxUnpushedCommits int
	MaxBlobs           int
	MaxFindings        int
	Timeout            time.Duration
	// Gitleaks additionally runs gitleaks over the working tree when it is on
	// PATH. It is off by default: a backend that switches itself on where the
	// binary happens to be installed makes two machines disagree about the
	// same repository for reasons nobody can see.
	Gitleaks bool
}

func (o Options) withDefaults() Options {
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = DefaultMaxFileBytes
	}
	if o.MaxWorktreeFiles <= 0 {
		o.MaxWorktreeFiles = DefaultMaxWorktreeFiles
	}
	if o.MaxUnpushedCommits <= 0 {
		o.MaxUnpushedCommits = DefaultMaxUnpushedCommits
	}
	if o.MaxBlobs <= 0 {
		o.MaxBlobs = DefaultMaxBlobs
	}
	if o.MaxFindings <= 0 {
		o.MaxFindings = DefaultMaxFindings
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	return o
}

// blob is one object to read, and where it came from.
type blob struct {
	sha string
	loc secrets.Location
}

type scanner struct {
	dir  string
	g    *gitx.Runner
	opts Options
	rep  secrets.Report
	// full stops further recording once the finding limit is reached, so a
	// repository that is one big leak cannot fill the state document.
	full bool
}

// Repo scans one repository and returns what it found and what it skipped.
//
// A failure of any single stage degrades the report rather than failing it:
// finding secrets in the working tree is worth reporting even if the stash
// walk broke.
func Repo(ctx context.Context, dir string, opts Options) secrets.Report {
	opts = opts.withDefaults()

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	s := &scanner{
		dir:  dir,
		g:    gitx.New(dir),
		opts: opts,
		rep:  secrets.Report{Backend: backendName(opts), ScannedAt: time.Now().UTC()},
	}

	var problems []string
	note := func(what string, err error) {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			problems = append(problems, what+": "+err.Error())
		}
	}

	note("worktree", s.worktree(ctx))
	if opts.Gitleaks {
		note("gitleaks", s.gitleaks(ctx))
	}

	pending, err := s.stashBlobs(ctx)
	note("stash", err)
	unpushed, err := s.unpushedBlobs(ctx)
	note("unpushed", err)
	note("objects", s.blobs(ctx, append(pending, unpushed...)))

	if ctx.Err() != nil {
		s.rep.Note(secrets.SkipDeadline, 1, int(opts.Timeout.Seconds()))
	}

	sort.Slice(s.rep.Findings, func(i, j int) bool { return less(s.rep.Findings[i], s.rep.Findings[j]) })
	s.rep.Error = strings.Join(problems, "; ")
	return s.rep
}

// Staged scans the content a commit is about to record. It is what the
// pre-commit hook runs, and it reads the index rather than the working tree so
// that partially staged changes are judged as they will be committed.
func Staged(ctx context.Context, dir string, opts Options) secrets.Report {
	opts = opts.withDefaults()

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	s := &scanner{
		dir:  dir,
		g:    gitx.New(dir),
		opts: opts,
		rep:  secrets.Report{Backend: "builtin", ScannedAt: time.Now().UTC()},
	}

	fields, err := s.g.RunZ(ctx, "diff", "--cached", "--raw", "-z", "--no-abbrev", "--no-color", "--diff-filter=AM")
	if err != nil {
		s.rep.Error = err.Error()
		return s.rep
	}
	if err := s.blobs(ctx, parseRaw(fields, secrets.OriginStaged, "")); err != nil {
		s.rep.Error = err.Error()
	}

	sort.Slice(s.rep.Findings, func(i, j int) bool { return less(s.rep.Findings[i], s.rep.Findings[j]) })
	return s.rep
}

func backendName(o Options) string {
	if o.Gitleaks {
		return "builtin+gitleaks"
	}
	return "builtin"
}

func less(a, b secrets.Finding) bool {
	switch {
	case a.Origin() != b.Origin():
		return a.Origin() < b.Origin()
	case a.Ref() != b.Ref():
		return a.Ref() < b.Ref()
	case a.Path() != b.Path():
		return a.Path() < b.Path()
	case a.Line() != b.Line():
		return a.Line() < b.Line()
	}
	return a.Rule() < b.Rule()
}

// record adds the hits found in one piece of content, stopping at the finding
// limit and saying so.
func (s *scanner) record(loc secrets.Location, hits []secrets.Hit) {
	if s.full || len(hits) == 0 {
		return
	}
	room := s.opts.MaxFindings - len(s.rep.Findings)
	if room <= 0 {
		s.full = true
		s.rep.Note(secrets.SkipFindings, len(hits), s.opts.MaxFindings)
		return
	}
	if len(hits) > room {
		s.rep.Note(secrets.SkipFindings, len(hits)-room, s.opts.MaxFindings)
		hits = hits[:room]
		s.full = true
	}
	s.rep.Findings = append(s.rep.Findings, secrets.Findings(loc, hits)...)
}

// worktree scans the checked-out files: everything git tracks, plus untracked
// files it would let you commit. Ignored files are deliberately left alone:
// .gitignore is poppy's secrets boundary everywhere else, and a scanner that
// read past it would be reporting on files the rest of the tool promises never
// to touch.
func (s *scanner) worktree(ctx context.Context) error {
	paths, err := s.g.RunZ(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--deduplicate")
	if err != nil {
		return err
	}
	if len(paths) > s.opts.MaxWorktreeFiles {
		s.rep.Note(secrets.SkipWorktreeFiles, len(paths)-s.opts.MaxWorktreeFiles, s.opts.MaxWorktreeFiles)
		paths = paths[:s.opts.MaxWorktreeFiles]
	}

	for _, p := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == "" {
			continue
		}
		content, ok := s.readFile(filepath.Join(s.dir, p))
		if !ok {
			continue
		}
		s.rep.Counts.WorktreeFiles++
		s.record(secrets.Location{Origin: secrets.OriginWorktree, Path: p}, secrets.Scan(p, content))
	}
	return nil
}

// readFile reads a working-tree file, reporting why it was skipped when it
// cannot be scanned. A path git lists but that is not on disk is a deletion
// staged or made in the working tree, not a fault.
func (s *scanner) readFile(path string) ([]byte, bool) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, false
	case err != nil:
		s.rep.Note(secrets.SkipUnreadable, 1, 0)
		return nil, false
	case !info.Mode().IsRegular():
		return nil, false
	case info.Size() > s.opts.MaxFileBytes:
		s.rep.Note(secrets.SkipFileTooLarge, 1, int(s.opts.MaxFileBytes))
		return nil, false
	}

	content, err := os.ReadFile(path)
	if err != nil {
		s.rep.Note(secrets.SkipUnreadable, 1, 0)
		return nil, false
	}
	if secrets.IsBinary(content) {
		s.rep.Note(secrets.SkipBinary, 1, 0)
		return nil, false
	}
	return content, true
}

// stashBlobs lists the objects held in stashes. A stash commit has HEAD as its
// first parent, the index as its second, and (when the stash was taken with
// untracked files) a tree of those as its third. Diffing against the first
// parent yields what the stash actually holds; the third parent's tree is
// listed whole, since none of it exists anywhere else at all.
func (s *scanner) stashBlobs(ctx context.Context) ([]blob, error) {
	shas, err := s.g.Lines(ctx, "rev-list", "--walk-reflogs", "refs/stash")
	if err != nil {
		// No stash ref exists. An empty stash is not a failure.
		return nil, nil
	}

	var out []blob
	for i, sha := range shas {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		ref := fmt.Sprintf("stash@{%d}", i)
		s.rep.Counts.Stashes++

		parents, err := s.g.Run(ctx, "rev-list", "--parents", "-n", "1", sha)
		if err != nil {
			return out, err
		}
		fields := strings.Fields(parents)
		if len(fields) < 2 {
			continue
		}

		changed, err := s.diffBlobs(ctx, secrets.OriginStash, ref, fields[1], sha)
		if err != nil {
			return out, err
		}
		out = append(out, changed...)

		if len(fields) >= 4 {
			untracked, err := s.treeBlobs(ctx, secrets.OriginStash, ref, fields[3])
			if err != nil {
				return out, err
			}
			out = append(out, untracked...)
		}
	}
	return out, nil
}

// unpushedBlobs lists the objects introduced by commits on local branches that
// no remote-tracking branch reaches. In a repository with no remote at all
// that is the entire history, which is correct (such a repository exists on
// one disk) and is what the commit bound is for.
func (s *scanner) unpushedBlobs(ctx context.Context) ([]blob, error) {
	total, err := s.g.Run(ctx, "rev-list", "--count", "--branches", "--not", "--remotes")
	if err != nil {
		return nil, nil // an unborn HEAD has no commits to walk
	}
	n, _ := strconv.Atoi(strings.TrimSpace(total))
	if n == 0 {
		return nil, nil
	}
	if n > s.opts.MaxUnpushedCommits {
		s.rep.Note(secrets.SkipUnpushedCommits, n-s.opts.MaxUnpushedCommits, s.opts.MaxUnpushedCommits)
	}

	shas, err := s.g.Lines(ctx, "rev-list", "--branches", "--not", "--remotes",
		"--max-count="+strconv.Itoa(s.opts.MaxUnpushedCommits))
	if err != nil {
		return nil, err
	}

	var out []blob
	for _, sha := range shas {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		s.rep.Counts.UnpushedCommits++
		// --root so the very first commit of a repository yields its files
		// rather than an empty diff.
		added, err := s.diffBlobs(ctx, secrets.OriginCommit, sha, "--root", sha)
		if err != nil {
			return out, err
		}
		out = append(out, added...)
	}
	return out, nil
}

// diffBlobs lists the blobs a change introduces, in git's raw diff format:
// ":<srcmode> <dstmode> <srcsha> <dstsha> <status>" followed by the path.
func (s *scanner) diffBlobs(ctx context.Context, origin secrets.Origin, ref string, revs ...string) ([]blob, error) {
	args := append([]string{"diff-tree", "-r", "-z", "--no-abbrev", "--no-commit-id", "--diff-filter=AM"}, revs...)
	fields, err := s.g.RunZ(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseRaw(fields, origin, ref), nil
}

// parseRaw reads git's raw diff format, which alternates a metadata record
// with the path it describes when -z is in force. Object names must be
// unabbreviated: the porcelain diff shortens them by default, and a short name
// does not match the full one cat-file reports back.
func parseRaw(fields []string, origin secrets.Origin, ref string) []blob {
	var out []blob
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.Fields(fields[i])
		if len(meta) < 5 {
			continue
		}
		out = append(out, blob{
			sha: meta[3],
			loc: secrets.Location{Origin: origin, Ref: ref, Path: fields[i+1]},
		})
	}
	return out
}

// treeBlobs lists every blob in a tree, as "<mode> <type> <sha>\t<path>".
func (s *scanner) treeBlobs(ctx context.Context, origin secrets.Origin, ref, tree string) ([]blob, error) {
	entries, err := s.g.RunZ(ctx, "ls-tree", "-r", "-z", tree)
	if err != nil {
		return nil, err
	}

	var out []blob
	for _, e := range entries {
		meta, path, ok := strings.Cut(e, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		out = append(out, blob{sha: fields[2], loc: secrets.Location{Origin: origin, Ref: ref, Path: path}})
	}
	return out, nil
}

// blobs scans object contents, reading them from git in batches.
//
// Objects are deduplicated by SHA first, which is where most of the saving on
// a real repository comes from: a file edited across fifty unpushed commits
// contributes fifty entries and a handful of distinct blobs.
func (s *scanner) blobs(ctx context.Context, wanted []blob) error {
	seen := make(map[string]bool, len(wanted))
	unique := wanted[:0]
	for _, b := range wanted {
		if seen[b.sha] {
			continue
		}
		seen[b.sha] = true
		unique = append(unique, b)
	}
	if len(unique) > s.opts.MaxBlobs {
		s.rep.Note(secrets.SkipBlobs, len(unique)-s.opts.MaxBlobs, s.opts.MaxBlobs)
		unique = unique[:s.opts.MaxBlobs]
	}
	if len(unique) == 0 {
		return nil
	}

	sizes, err := s.blobSizes(ctx, unique)
	if err != nil {
		return err
	}

	batch := make([]blob, 0, blobBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := s.readBatch(ctx, batch)
		batch = batch[:0]
		return err
	}

	for _, b := range unique {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		size, ok := sizes[b.sha]
		switch {
		case !ok:
			continue
		case size > s.opts.MaxFileBytes:
			s.rep.Note(secrets.SkipFileTooLarge, 1, int(s.opts.MaxFileBytes))
			continue
		}
		batch = append(batch, b)
		if len(batch) == blobBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// blobSizes reads object sizes in one pass so oversized objects are dropped
// before their contents are ever read into memory.
func (s *scanner) blobSizes(ctx context.Context, blobs []blob) (map[string]int64, error) {
	var in strings.Builder
	for _, b := range blobs {
		in.WriteString(b.sha)
		in.WriteByte('\n')
	}
	lines, err := s.g.WithStdin([]byte(in.String())).Lines(ctx, "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}

	sizes := make(map[string]int64, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		n, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		sizes[fields[0]] = n
	}
	return sizes, nil
}

// readBatch reads one batch of object contents and scans each. cat-file
// --batch emits "<sha> <type> <size>\n<contents>\n" per request, so the
// contents are framed by the declared length rather than by any delimiter,
// which is what makes it safe for arbitrary file content.
func (s *scanner) readBatch(ctx context.Context, batch []blob) error {
	var in strings.Builder
	for _, b := range batch {
		in.WriteString(b.sha)
		in.WriteByte('\n')
	}
	out, err := s.g.WithStdin([]byte(in.String())).Bytes(ctx, "cat-file", "--batch")
	if err != nil {
		return err
	}

	contents := make(map[string][]byte, len(batch))
	for rest := out; len(rest) > 0; {
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			break
		}
		header := string(rest[:nl])
		rest = rest[nl+1:]

		fields := strings.Fields(header)
		if len(fields) < 3 || fields[1] != "blob" {
			break // "<sha> missing" or an unexpected type; the framing is lost
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size > len(rest) {
			break
		}
		contents[fields[0]] = rest[:size]
		rest = rest[size:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
	}

	for _, b := range batch {
		content, ok := contents[b.sha]
		if !ok {
			continue
		}
		if secrets.IsBinary(content) {
			s.rep.Note(secrets.SkipBinary, 1, 0)
			continue
		}
		s.rep.Counts.Blobs++
		s.record(b.loc, secrets.Scan(b.loc.Path, content))
	}
	return nil
}

// Fingerprint summarizes the repository state a scan would cover. A cycle that
// sees the same fingerprint can carry the previous report forward rather than
// re-reading every file, which is what keeps a five-minute scan interval
// affordable across a hundred repositories.
func Fingerprint(r model.Repo) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00", r.Head.SHA, r.Dirty.Total(), r.Stashes)
	names := make([]string, 0, len(r.Branches))
	for _, b := range r.Branches {
		names = append(names, b.Name+" "+b.SHA)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00", n)
	}
	for _, f := range r.Dirty.Files {
		fmt.Fprintf(h, "%s\x00", f)
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}
