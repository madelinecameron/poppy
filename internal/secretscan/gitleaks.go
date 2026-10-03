package secretscan

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/madelinecameron/poppy/internal/secrets"
)

// GitleaksAvailable reports whether the optional stronger backend is installed.
func GitleaksAvailable() bool {
	_, err := exec.LookPath("gitleaks")
	return err == nil
}

// gitleaksFinding is the subset of gitleaks' report poppy reads. Its report
// also carries Secret and Match fields holding the credential itself; they are
// absent from this struct on purpose, so the value is discarded by the JSON
// decoder and never exists as a Go value poppy could accidentally keep.
type gitleaksFinding struct {
	RuleID    string `json:"RuleID"`
	File      string `json:"File"`
	StartLine int    `json:"StartLine"`
}

// gitleaks runs gitleaks over the working tree and folds its findings in
// alongside the built-in ones.
//
// It covers the working tree only. The stash and unpushed-commit walks stay on
// the built-in rules, because feeding gitleaks the contents of an arbitrary
// object would mean writing that content somewhere it can read, which is
// precisely the thing this feature must not do.
func (s *scanner) gitleaks(ctx context.Context) error {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "poppy-gitleaks-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	report := filepath.Join(dir, "report.json")

	// --redact so the credential never reaches the report file at all, and
	// --exit-code 0 so findings are not reported to us as a command failure.
	cmd := exec.CommandContext(ctx, "gitleaks", "detect",
		"--no-git", "--no-banner", "--redact",
		"--exit-code", "0",
		"--report-format", "json",
		"--report-path", report,
		"--source", s.dir)
	cmd.Dir = s.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return &gitleaksError{err: err, output: strings.TrimSpace(string(out))}
	}

	data, err := os.ReadFile(report)
	if err != nil {
		return err
	}
	for _, f := range parseGitleaks(data, s.dir) {
		s.record(f.loc, []secrets.Hit{f.hit})
	}
	return nil
}

type gitleaksError struct {
	err    error
	output string
}

func (e *gitleaksError) Error() string {
	if e.output == "" {
		return e.err.Error()
	}
	// Only the first line: gitleaks prints its own findings summary, and the
	// rest of its output has no business being kept.
	if i := strings.IndexByte(e.output, '\n'); i >= 0 {
		return e.output[:i]
	}
	return e.output
}

func (e *gitleaksError) Unwrap() error { return e.err }

type gitleaksHit struct {
	loc secrets.Location
	hit secrets.Hit
}

// parseGitleaks reads a gitleaks JSON report, keeping the rule, the path, and
// the line and dropping everything else the file contains.
func parseGitleaks(data []byte, root string) []gitleaksHit {
	var raw []gitleaksFinding
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}

	out := make([]gitleaksHit, 0, len(raw))
	for _, f := range raw {
		path := f.File
		if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
		out = append(out, gitleaksHit{
			loc: secrets.Location{Origin: secrets.OriginWorktree, Path: filepath.ToSlash(path)},
			hit: secrets.Hit{Rule: gitleaksRule(f.RuleID), Line: f.StartLine},
		})
	}
	return out
}

// gitleaksRule namespaces and sanitizes a rule identifier from an external
// tool. Poppy's own rule identifiers come from a closed set; one arriving from
// a file cannot be trusted to be an identifier at all, so it is reduced to the
// characters an identifier may contain and bounded in length.
func gitleaksRule(id string) secrets.RuleID {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		case r == '_' || r == ' ':
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	if b.Len() == 0 {
		return "gitleaks:unnamed"
	}
	return secrets.RuleID("gitleaks:" + b.String())
}
