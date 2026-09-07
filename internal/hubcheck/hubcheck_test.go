package hubcheck

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/gittest"
)

// scratchHub builds a bare repository with one commit in it, standing in for
// the fleet hub. Nothing here ever touches the real one.
func scratchHub(t *testing.T) string {
	t.Helper()
	bare := gittest.NewBare(t, "state")
	work := gittest.New(t, "state")
	work.Commit("one", "a.txt", "hello\n")
	work.Git("push", "-q", bare, "state")
	return bare
}

// looseObject returns a path under objects/<xx>/, and only from there: the
// files git also keeps under objects/ — a commit-graph, a pack — are not
// objects, and emptying one of those would test something else.
func looseObject(t *testing.T, repo string) string {
	t.Helper()
	fanouts, err := os.ReadDir(filepath.Join(repo, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fanout := range fanouts {
		name := fanout.Name()
		if !fanout.IsDir() || len(name) != 2 || strings.Trim(name, "0123456789abcdef") != "" {
			continue
		}
		objects, err := os.ReadDir(filepath.Join(repo, "objects", name))
		if err != nil {
			t.Fatal(err)
		}
		if len(objects) > 0 {
			return filepath.Join(repo, "objects", name, objects[0].Name())
		}
	}
	t.Fatal("the scratch hub has no loose object to wreck")
	return ""
}

func run(t *testing.T, repo string, fsck bool) *Result {
	t.Helper()
	return Run(context.Background(), Options{Repo: repo, Fsck: fsck, Timeout: time.Minute})
}

func TestACleanHubReportsThatBothChecksLooked(t *testing.T) {
	r := run(t, scratchHub(t), true)

	if got := r.Verdict(); got != StatusClean {
		t.Fatalf("verdict = %s, want clean (reason %q, sweep %s, fsck %s)",
			got, r.Reason, r.Sweep.Status, r.Fsck.Status)
	}
	if !r.Sweep.Status.Ran() || !r.Fsck.Status.Ran() {
		t.Errorf("sweep %s / fsck %s: a clean verdict must rest on checks that ran",
			r.Sweep.Status, r.Fsck.Status)
	}
	if r.ZeroCount != 0 {
		t.Errorf("zeroCount = %d, want 0", r.ZeroCount)
	}
}

// This is the incident, reduced. Zero-length files sat in the pack directory
// and git carried on: fsck passed, refs resolved, every git command succeeded.
// Only something looking at the files on disk can see them, which is why the
// sweep and not the fsck is the check that carries this ticket.
func TestTheSweepSeesCorruptionGitReportsNothingAbout(t *testing.T) {
	repo := scratchHub(t)
	packDir := filepath.Join(repo, "objects", "pack")
	for _, name := range []string{
		"tmp_pack_abc123",
		"pack-0000000000000000000000000000000000000000.idx",
	} {
		if err := os.WriteFile(filepath.Join(packDir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := run(t, repo, true)

	if r.Fsck.Status != StatusClean {
		t.Errorf("fsck = %s; the point of this case is that git is content", r.Fsck.Status)
	}
	if r.Sweep.Status != StatusFound {
		t.Fatalf("sweep = %s, want found: the corruption is on disk", r.Sweep.Status)
	}
	if r.Verdict() != StatusFound {
		t.Errorf("verdict = %s, want found", r.Verdict())
	}
	if r.ZeroCount != 2 {
		t.Errorf("zeroCount = %d, want 2", r.ZeroCount)
	}
	for _, want := range []string{"tmp_pack_abc123", "pack-0000000"} {
		if !strings.Contains(strings.Join(r.Zero, "\n"), want) {
			t.Errorf("zero paths %v do not name %s", r.Zero, want)
		}
	}
	if r.Reason == "" {
		t.Error("a finding with no reason gives a reader nothing to act on")
	}
}

func TestAWreckedObjectIsCaughtByBothChecks(t *testing.T) {
	repo := scratchHub(t)
	loose := looseObject(t, repo)
	// Loose objects are written read-only, so emptying one takes a chmod
	// first. A power loss is not so polite.
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(loose, 0); err != nil {
		t.Fatal(err)
	}

	r := run(t, repo, true)

	if r.Sweep.Status != StatusFound {
		t.Errorf("sweep = %s, want found", r.Sweep.Status)
	}
	if r.Fsck.Status != StatusFound {
		t.Errorf("fsck = %s, want found", r.Fsck.Status)
	}
	if len(r.Fsck.Detail) == 0 {
		t.Error("fsck found something and said nothing about what")
	}
}

func TestAMissingRepositoryFailsRatherThanPassing(t *testing.T) {
	r := run(t, filepath.Join(t.TempDir(), "absent.git"), true)

	if r.Verdict() != StatusFailed {
		t.Fatalf("verdict = %s, want failed", r.Verdict())
	}
	if r.Sweep.Status.Ran() {
		t.Error("the sweep claims it ran against a repository that is not there")
	}
}

func TestAPathThatIsNotARepositoryFailsTheFsck(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := run(t, dir, true)

	// The sweep can honestly say it found no zero-length files. The fsck
	// cannot say anything, and must not borrow the sweep's answer.
	if r.Sweep.Status != StatusClean {
		t.Errorf("sweep = %s, want clean", r.Sweep.Status)
	}
	if r.Fsck.Status != StatusFailed {
		t.Errorf("fsck = %s, want failed", r.Fsck.Status)
	}
	if r.Verdict() == StatusClean {
		t.Error("verdict is clean while half the check could not run")
	}
}

// A check that did not run must never present as a check that ran and found
// nothing. This is the requirement the whole ticket rests on, so it is asserted
// against every status that means absence rather than against one of them.
func TestACheckThatDidNotRunIsNeverClean(t *testing.T) {
	for _, absent := range []Status{StatusUnknown, StatusFailed, StatusSkipped, StatusNotConfigured} {
		t.Run(absent.String(), func(t *testing.T) {
			if absent.Ran() {
				t.Errorf("%s.Ran() = true", absent)
			}

			neither := &Result{Sweep: Check{Status: absent}, Fsck: Check{Status: absent}}
			if got := neither.Verdict(); got == StatusClean {
				t.Errorf("verdict = clean when neither check ran (%s)", absent)
			}

			// The half-and-half case is the subtle one: one real answer is
			// enough to make a report look answered.
			half := &Result{Sweep: Check{Status: StatusClean}, Fsck: Check{Status: absent}}
			if got := half.Verdict(); got == StatusClean {
				t.Errorf("verdict = clean when the fsck did not run (%s)", absent)
			}
			otherHalf := &Result{Sweep: Check{Status: absent}, Fsck: Check{Status: StatusClean}}
			if got := otherHalf.Verdict(); got == StatusClean {
				t.Errorf("verdict = clean when the sweep did not run (%s)", absent)
			}
		})
	}
}

func TestAnAbsentResultIsUnknownRatherThanHealthy(t *testing.T) {
	// A machine whose document predates this check carries no result at all.
	var missing *Result
	if got := missing.Verdict(); got != StatusUnknown {
		t.Errorf("verdict of a nil result = %s, want unknown", got)
	}
	if missing.Corrupt() {
		t.Error("a nil result claims corruption")
	}
	if StatusUnknown.String() != "not-run" {
		t.Errorf("the zero status renders as %q, which reads as no answer at all", StatusUnknown.String())
	}
}

func TestASkippedFsckIsRecordedWithItsReason(t *testing.T) {
	r := Run(context.Background(), Options{
		Repo:           scratchHub(t),
		Fsck:           false,
		FsckSkipReason: "last full fsck was 4m ago",
		Timeout:        time.Minute,
	})

	if r.Sweep.Status != StatusClean {
		t.Errorf("sweep = %s, want clean", r.Sweep.Status)
	}
	if r.Fsck.Status != StatusSkipped {
		t.Fatalf("fsck = %s, want skipped", r.Fsck.Status)
	}
	if r.Verdict() == StatusClean {
		t.Error("verdict is clean while the fsck was skipped")
	}
	if len(r.Fsck.Detail) == 0 || !strings.Contains(r.Fsck.Detail[0], "4m ago") {
		t.Errorf("fsck detail = %v, want the reason it was skipped", r.Fsck.Detail)
	}
}

// An ssh connection that dies mid-stream delivers a short but well-formed
// report. Without the sentinel, output that stopped just after a clean sweep
// line would be read as a clean hub.
func TestATruncatedReportIsTreatedAsNotRun(t *testing.T) {
	full := "poppy-hubcheck 1\nhost h\nzero-count 0\nsweep clean\nfsck clean\nend\n"
	truncated := "poppy-hubcheck 1\nhost h\nzero-count 0\nsweep clean\n"
	headless := "zero-count 0\nsweep clean\nfsck clean\nend\n"

	var whole Result
	parse(&whole, full)
	if whole.Verdict() != StatusClean {
		t.Fatalf("a complete report parsed to %s, want clean", whole.Verdict())
	}

	for name, out := range map[string]string{"truncated": truncated, "no header": headless} {
		var r Result
		parse(&r, out)
		if r.Verdict() != StatusFailed {
			t.Errorf("%s report parsed to %s, want failed", name, r.Verdict())
		}
		if r.Reason == "" {
			t.Errorf("%s report gives no reason", name)
		}
	}
}

func TestACheckThatCouldNotBeRunAtAllIsFailed(t *testing.T) {
	// A timeout that expires before the shell can start is the cheapest
	// deterministic stand-in for an unreachable hub host.
	r := Run(context.Background(), Options{Repo: scratchHub(t), Fsck: true, Timeout: time.Nanosecond})

	if r.Verdict() != StatusFailed {
		t.Fatalf("verdict = %s, want failed", r.Verdict())
	}
	if r.Reason == "" {
		t.Error("a failed check must say what stopped it")
	}
	if r.CheckedAt.IsZero() {
		t.Error("a failed attempt still happened at a time, and that time is what says poppy is still trying")
	}
}

func TestNoRepositoryToCheckIsNotConfiguredRatherThanFailed(t *testing.T) {
	r := Run(context.Background(), Options{})

	if r.Verdict() != StatusNotConfigured {
		t.Errorf("verdict = %s, want not-configured", r.Verdict())
	}
}

func TestTheDetailAndPathListsAreBounded(t *testing.T) {
	repo := scratchHub(t)
	packDir := filepath.Join(repo, "objects", "pack")
	const made = maxDetail + 25
	for i := range made {
		name := filepath.Join(packDir, "tmp_pack_"+strconv.Itoa(i))
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := run(t, repo, false)

	if r.ZeroCount != made {
		t.Errorf("zeroCount = %d, want the true total %d", r.ZeroCount, made)
	}
	if len(r.Zero) > maxDetail {
		t.Errorf("reported %d paths, want at most %d", len(r.Zero), maxDetail)
	}
	if !r.Sweep.Truncated {
		t.Error("the list was cut short and the report does not say so")
	}
}

func TestRepoPathFromRemote(t *testing.T) {
	for remote, want := range map[string]string{
		"ssh://fleet@backoffice.example.ts.net/opt/fleet/poppy-hub.git": "/opt/fleet/poppy-hub.git",
		"mads@backoffice:/opt/fleet/poppy-hub.git":                      "/opt/fleet/poppy-hub.git",
		"/srv/hub.git":                "/srv/hub.git",
		"https://example.com/hub.git": "",
		"":                            "",
	} {
		if got := RepoPathFromRemote(remote); got != want {
			t.Errorf("RepoPathFromRemote(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestScriptQuotesThePathItIsGiven(t *testing.T) {
	s := Script("/opt/fleet/it's here.git", true, "")
	if !strings.Contains(s, `repo='/opt/fleet/it'\''s here.git'`) {
		t.Errorf("the repository path is not quoted for the shell:\n%s", s[:200])
	}
}
