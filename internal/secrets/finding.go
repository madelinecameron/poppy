// Package secrets detects credentials in file content and records where they
// are, never what they are.
//
// Poppy's state documents are committed to a shared hub, and the rule the hub
// is built on is that documents record paths and counts and never file
// contents. A scanner that leaked a matched credential into a state document
// would be strictly worse than no scanner at all: it would copy every secret
// it found into a repository that is pushed off the machine.
//
// The guarantee is structural rather than a matter of care in the writer.
// Matching happens inside this package against a []byte that is never
// returned; a rule reports a line number and its own identity, and the only
// constructor for a Finding takes a location and a rule. No exported function
// in this package accepts content and returns anything derived from it beyond
// a position, so there is no expression anywhere in poppy that can put a
// matched value into a state document.
package secrets

import (
	"encoding/json"
	"fmt"
	"time"
)

// RuleID names a detection rule. Values are the constants declared in
// rules.go; the set is closed so a rule identifier can never be scanned text.
type RuleID string

// Origin is the kind of content a finding was made in. The three that are not
// the working tree are the point of the feature: they hold content that exists
// in exactly one place and that no pre-commit hook will ever see.
type Origin string

const (
	// OriginWorktree is a file in the checked-out working tree.
	OriginWorktree Origin = "worktree"
	// OriginStaged is content staged in the index, scanned by the pre-commit
	// hook before it becomes a commit.
	OriginStaged Origin = "staged"
	// OriginStash is content held in a stash, which no remote has ever seen.
	OriginStash Origin = "stash"
	// OriginCommit is content in a commit on a local branch that is not
	// reachable from any remote-tracking branch.
	OriginCommit Origin = "commit"
)

// Location is where scanned content came from. It names content; it never
// carries any.
type Location struct {
	Origin Origin
	// Ref identifies the container: a stash selector such as "stash@{0}" or a
	// commit SHA. It is empty for the working tree and the index.
	Ref string
	// Path is the repository-relative path of the file.
	Path string
}

// Hit is one rule match. It carries the line the rule matched on and nothing
// from that line.
type Hit struct {
	Rule RuleID
	Line int
}

// Finding is where a credential was detected, and only where.
//
// Its fields are unexported and it has no constructor that accepts content, so
// a matched value cannot be stored in one even by mistake. Marshalling is
// explicit for the same reason: the JSON shape is the whole shape, and a
// document read back from the hub with an extra field carrying a value drops
// it on unmarshal rather than propagating it.
type Finding struct {
	rule   RuleID
	origin Origin
	ref    string
	path   string
	line   int
}

// Rule reports which detection rule matched.
func (f Finding) Rule() RuleID { return f.rule }

// Origin reports what kind of content the finding is in.
func (f Finding) Origin() Origin { return f.origin }

// Ref reports the stash selector or commit SHA holding the content, empty for
// the working tree.
func (f Finding) Ref() string { return f.ref }

// Path reports the repository-relative path of the file.
func (f Finding) Path() string { return f.path }

// Line reports the 1-based line the rule matched on.
func (f Finding) Line() int { return f.line }

// Where renders the finding's position for a person, without its value.
func (f Finding) Where() string {
	if f.ref == "" {
		return fmt.Sprintf("%s:%d", f.path, f.line)
	}
	return fmt.Sprintf("%s:%s:%d", f.ref, f.path, f.line)
}

// findingJSON is the wire shape. It is deliberately the complete shape.
type findingJSON struct {
	Rule   RuleID `json:"rule"`
	Origin Origin `json:"origin"`
	Ref    string `json:"ref,omitempty"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
}

// MarshalJSON writes the location and the rule. There is nothing else to write.
func (f Finding) MarshalJSON() ([]byte, error) {
	return json.Marshal(findingJSON{
		Rule:   f.rule,
		Origin: f.origin,
		Ref:    f.ref,
		Path:   f.path,
		Line:   f.line,
	})
}

// UnmarshalJSON reads only the known fields, so any extra field a
// differently-versioned or misbehaving writer added is discarded here rather
// than carried further.
func (f *Finding) UnmarshalJSON(data []byte) error {
	var j findingJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	f.rule, f.origin, f.ref, f.path, f.line = j.Rule, j.Origin, j.Ref, j.Path, j.Line
	return nil
}

// Findings pairs a batch of hits with the location the scanned content came
// from. It is the only way to build a Finding, and it takes no content.
func Findings(loc Location, hits []Hit) []Finding {
	out := make([]Finding, 0, len(hits))
	for _, h := range hits {
		out = append(out, Finding{
			rule:   h.Rule,
			origin: loc.Origin,
			ref:    loc.Ref,
			path:   loc.Path,
			line:   h.Line,
		})
	}
	return out
}

// SkipReason explains why part of a repository was not scanned. The set is
// closed so a skip can never carry scanned text either.
type SkipReason string

const (
	SkipBinary          SkipReason = "binary"
	SkipFileTooLarge    SkipReason = "file-too-large"
	SkipUnreadable      SkipReason = "unreadable"
	SkipWorktreeFiles   SkipReason = "worktree-file-limit"
	SkipUnpushedCommits SkipReason = "unpushed-commit-limit"
	SkipBlobs           SkipReason = "blob-limit"
	SkipFindings        SkipReason = "finding-limit"
	SkipDeadline        SkipReason = "deadline"
)

// Skip records work a scan declined to do. A bound that silently truncates is
// worse than one that reports itself: a quiet repository must mean a clean
// repository, not an exhausted budget.
type Skip struct {
	Reason SkipReason `json:"reason"`
	Count  int        `json:"count"`
	Limit  int        `json:"limit,omitempty"`
}

// Explain renders a skip as a sentence for the CLI.
func (s Skip) Explain() string {
	switch s.Reason {
	case SkipBinary:
		return fmt.Sprintf("%d binary file(s) not scanned", s.Count)
	case SkipFileTooLarge:
		return fmt.Sprintf("%d file(s) over the size limit not scanned", s.Count)
	case SkipUnreadable:
		return fmt.Sprintf("%d file(s) could not be read", s.Count)
	case SkipWorktreeFiles:
		return fmt.Sprintf("%d working-tree file(s) beyond the limit of %d not scanned", s.Count, s.Limit)
	case SkipUnpushedCommits:
		return fmt.Sprintf("%d unpushed commit(s) beyond the limit of %d not scanned", s.Count, s.Limit)
	case SkipBlobs:
		return fmt.Sprintf("%d stash/commit blob(s) beyond the limit of %d not scanned", s.Count, s.Limit)
	case SkipFindings:
		return fmt.Sprintf("%d finding(s) beyond the limit of %d not recorded", s.Count, s.Limit)
	case SkipDeadline:
		return fmt.Sprintf("scan stopped at its deadline with %d item(s) unexamined", s.Count)
	}
	return string(s.Reason)
}

// Counts records how much content a scan examined, which is what makes a
// clean report meaningful.
type Counts struct {
	WorktreeFiles   int `json:"worktreeFiles"`
	Stashes         int `json:"stashes"`
	UnpushedCommits int `json:"unpushedCommits"`
	Blobs           int `json:"blobs"`
}

// Report is one repository's scan result.
type Report struct {
	// Backend names what did the matching, so a finding set can be compared
	// across machines that are not configured identically.
	Backend   string    `json:"backend"`
	ScannedAt time.Time `json:"scannedAt"`
	// Fingerprint summarizes the repository state the scan covered. A later
	// cycle that sees the same fingerprint can carry this report forward
	// instead of re-reading every file.
	Fingerprint string    `json:"fingerprint,omitempty"`
	Findings    []Finding `json:"findings,omitempty"`
	Counts      Counts    `json:"counts"`
	Skipped     []Skip    `json:"skipped,omitempty"`
	// Error records why a scan could not complete, if it could not.
	Error string `json:"error,omitempty"`
}

// Any reports whether anything was found.
func (r *Report) Any() bool { return r != nil && len(r.Findings) > 0 }

// Count reports how many findings were recorded.
func (r *Report) Count() int {
	if r == nil {
		return 0
	}
	return len(r.Findings)
}

// All lists the findings, tolerating a repository that was never scanned.
func (r *Report) All() []Finding {
	if r == nil {
		return nil
	}
	return r.Findings
}

// Skips lists what the scan declined to examine, tolerating a repository that
// was never scanned.
func (r *Report) Skips() []Skip {
	if r == nil {
		return nil
	}
	return r.Skipped
}

// Note records that part of the scan was skipped, merging into an existing
// entry for the same reason.
func (r *Report) Note(reason SkipReason, count, limit int) {
	if count <= 0 {
		return
	}
	for i := range r.Skipped {
		if r.Skipped[i].Reason == reason {
			r.Skipped[i].Count += count
			return
		}
	}
	r.Skipped = append(r.Skipped, Skip{Reason: reason, Count: count, Limit: limit})
}
