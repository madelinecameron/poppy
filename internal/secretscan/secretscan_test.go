package secretscan_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/secrets"
	"github.com/madelinecameron/poppy/internal/secretscan"
)

// fakeToken assembles a credential-shaped string at run time rather than
// writing one as a literal. A literal would make this file itself a finding
// every time poppy scans its own repository, which is the scanner working
// correctly and nobody wanting it.
func fakeToken() string { return "ghp_" + strings.Repeat("A1b2C3d4", 4) + "wxyz" }

func fakeAWSKey() string { return "AKIA" + "QWERTYUIOPASDFGH" }

func TestSecretInUnpushedCommitIsFound(t *testing.T) {
	repo := gittest.New(t, "main")
	remote := gittest.NewBare(t, "main")
	repo.Commit("init", "README.md", "nothing to see\n")
	repo.Git("remote", "add", "origin", remote)
	repo.Git("push", "-q", "-u", "origin", "main")

	repo.Git("checkout", "-q", "-b", "feature")
	repo.Commit("wire up the client", "client.go", "const token = \""+fakeToken()+"\"\n")
	// The leak is then removed from the working tree, so only the commit,
	// which no remote has, still holds it.
	repo.Commit("drop the literal", "client.go", "const token = os.Getenv(\"TOKEN\")\n")

	report := secretscan.Repo(context.Background(), repo.Path, secretscan.Options{})

	f := onlyFinding(t, report)
	if f.Origin() != secrets.OriginCommit {
		t.Errorf("origin = %q, want %q", f.Origin(), secrets.OriginCommit)
	}
	if f.Path() != "client.go" {
		t.Errorf("path = %q, want client.go", f.Path())
	}
	if f.Rule() != secrets.RuleGitHubToken {
		t.Errorf("rule = %q, want %q", f.Rule(), secrets.RuleGitHubToken)
	}
	if f.Ref() == "" {
		t.Error("finding has no commit ref")
	}
	assertNoValue(t, report, fakeToken())
}

func TestSecretInStashIsFound(t *testing.T) {
	t.Run("tracked file", func(t *testing.T) {
		repo := pushedRepo(t)
		repo.Write("config.yml", "aws_access_key_id: "+fakeAWSKey()+"\n")
		repo.Git("add", "-A")
		repo.Git("commit", "-q", "-m", "add config")
		repo.Write("config.yml", "aws_access_key_id: "+fakeAWSKey()+"-live\n")
		repo.Git("stash", "-q")

		// The commit that introduced it is itself unpushed, so restrict the
		// assertion to the stash finding.
		report := secretscan.Repo(context.Background(), repo.Path, secretscan.Options{})
		f := findingFrom(t, report, secrets.OriginStash)
		if f.Path() != "config.yml" {
			t.Errorf("path = %q, want config.yml", f.Path())
		}
		if f.Ref() != "stash@{0}" {
			t.Errorf("ref = %q, want stash@{0}", f.Ref())
		}
		assertNoValue(t, report, fakeAWSKey())
	})

	t.Run("untracked file", func(t *testing.T) {
		repo := pushedRepo(t)
		repo.Write("scratch.env", "SERVICE_TOKEN="+fakeToken()+"\n")
		repo.Git("stash", "-q", "-u")

		if got := worktreeFindings(secretscan.Repo(context.Background(), repo.Path, secretscan.Options{})); got != 0 {
			t.Fatalf("stash -u left %d working-tree finding(s); the file should be gone", got)
		}
		report := secretscan.Repo(context.Background(), repo.Path, secretscan.Options{})
		f := findingFrom(t, report, secrets.OriginStash)
		if f.Path() != "scratch.env" {
			t.Errorf("path = %q, want scratch.env", f.Path())
		}
		assertNoValue(t, report, fakeToken())
	})
}

func TestPushedAndCleanRepoIsQuiet(t *testing.T) {
	repo := pushedRepo(t)
	repo.Commit("add docs", "docs/setup.md", "Set API_TOKEN in your environment.\n")
	repo.Commit("add example env", ".env.example", "API_TOKEN=your-token-here\nDB_PASSWORD=changeme\n")
	repo.Git("push", "-q", "origin", "main")

	report := secretscan.Repo(context.Background(), repo.Path, secretscan.Options{})
	if report.Any() {
		t.Fatalf("clean repository reported %d finding(s): %v", report.Count(), report.Findings)
	}
	if report.Counts.WorktreeFiles == 0 {
		t.Error("nothing was scanned, so a quiet report means nothing")
	}
	if len(report.Skipped) != 0 {
		t.Errorf("unexpected skips: %v", report.Skipped)
	}
}

func TestSecretInWorkingTreeIsFound(t *testing.T) {
	repo := pushedRepo(t)
	repo.Write(".env", "DATABASE_PASSWORD=pXq7-Zv2Kd91mLw\n")

	report := secretscan.Repo(context.Background(), repo.Path, secretscan.Options{})
	f := findingFrom(t, report, secrets.OriginWorktree)
	if f.Rule() != secrets.RuleEnvAssignment {
		t.Errorf("rule = %q, want %q", f.Rule(), secrets.RuleEnvAssignment)
	}
	if f.Ref() != "" {
		t.Errorf("working-tree finding has ref %q, want none", f.Ref())
	}
}

func TestStagedScanReadsTheIndexNotTheWorkingTree(t *testing.T) {
	repo := pushedRepo(t)
	repo.Write("keys.txt", fakeAWSKey()+"\n")
	repo.Git("add", "keys.txt")
	// The working tree moves on after staging; the commit will still carry the
	// credential, which is what the hook has to catch.
	repo.Write("keys.txt", "removed\n")

	report := secretscan.Staged(context.Background(), repo.Path, secretscan.Options{})
	f := onlyFinding(t, report)
	if f.Origin() != secrets.OriginStaged {
		t.Errorf("origin = %q, want %q", f.Origin(), secrets.OriginStaged)
	}
	if f.Rule() != secrets.RuleAWSAccessKeyID {
		t.Errorf("rule = %q, want %q", f.Rule(), secrets.RuleAWSAccessKeyID)
	}
}

func TestBoundsAreReportedNotSilent(t *testing.T) {
	repo := gittest.New(t, "main")
	for i := range 5 {
		repo.Commit("commit", "file.txt", strings.Repeat("x", i+1)+"\n")
	}

	report := secretscan.Repo(context.Background(), repo.Path, secretscan.Options{MaxUnpushedCommits: 2})
	var skip *secrets.Skip
	for i := range report.Skipped {
		if report.Skipped[i].Reason == secrets.SkipUnpushedCommits {
			skip = &report.Skipped[i]
		}
	}
	if skip == nil {
		t.Fatalf("commit limit was applied without being reported: %v", report.Skipped)
	}
	if skip.Count != 3 || skip.Limit != 2 {
		t.Errorf("skip = %+v, want 3 unscanned beyond a limit of 2", *skip)
	}
	if report.Counts.UnpushedCommits != 2 {
		t.Errorf("scanned %d commits, want 2", report.Counts.UnpushedCommits)
	}
}

func TestFingerprintTracksRepositoryState(t *testing.T) {
	base := model.Repo{
		Head:     model.Head{SHA: "abc"},
		Branches: []model.Branch{{Name: "main", SHA: "abc"}},
		Stashes:  0,
	}
	moved := base
	moved.Stashes = 1

	if secretscan.Fingerprint(base) == secretscan.Fingerprint(moved) {
		t.Error("a new stash did not change the fingerprint, so it would not be rescanned")
	}
	if secretscan.Fingerprint(base) != secretscan.Fingerprint(base) {
		t.Error("fingerprint is not stable")
	}
}

// pushedRepo returns a repository whose main branch is fully pushed, so any
// finding a test provokes has a single unambiguous origin.
func pushedRepo(t *testing.T) *gittest.Repo {
	t.Helper()
	repo := gittest.New(t, "main")
	remote := gittest.NewBare(t, "main")
	repo.Commit("init", "README.md", "nothing to see\n")
	repo.Git("remote", "add", "origin", remote)
	repo.Git("push", "-q", "-u", "origin", "main")
	return repo
}

func onlyFinding(t *testing.T, r secrets.Report) secrets.Finding {
	t.Helper()
	if len(r.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %v (err %q)", len(r.Findings), r.Findings, r.Error)
	}
	return r.Findings[0]
}

func findingFrom(t *testing.T, r secrets.Report, origin secrets.Origin) secrets.Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Origin() == origin {
			return f
		}
	}
	t.Fatalf("no %s finding in %v (err %q)", origin, r.Findings, r.Error)
	return secrets.Finding{}
}

func worktreeFindings(r secrets.Report) int {
	n := 0
	for _, f := range r.Findings {
		if f.Origin() == secrets.OriginWorktree {
			n++
		}
	}
	return n
}

// assertNoValue is the guarantee the whole feature rests on: whatever the
// scanner found, the document it produces must not contain it.
func assertNoValue(t *testing.T, r secrets.Report, value string) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), value) {
		t.Fatal("the serialized report contains the matched credential")
	}
}
