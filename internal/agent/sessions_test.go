package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/sessions"
)

func hookEvent(t *testing.T, dir, id, cwd, event string) {
	t.Helper()
	h := sessions.Hook{Harness: sessions.Claude, Dir: dir}
	if err := h.Handle(context.Background(), sessions.Payload{SessionID: id, Cwd: cwd, Event: event}); err != nil {
		t.Fatalf("hook: %v", err)
	}
}

// A published session names each repository by the same RepoID the scan
// gives it, including a repository outside the scan roots.
func TestCyclePublishesSessionsWithRepoIDs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inside := newRepoAt(t, root, "poppy")
	inside.Commit("init", "a.txt", "one\n")
	inside.Git("remote", "add", "origin", "git@github.com:mads/poppy.git")

	outside := newRepoAt(t, t.TempDir(), "scratch")
	outside.Git("remote", "add", "origin", "git@github.com:mads/scratch.git")

	a := agentFor(t, "laptop", root, filepath.Join(t.TempDir(), "hub"), "")
	a.SessionsDir = t.TempDir()
	hookEvent(t, a.SessionsDir, "s1", inside.Path, sessions.EventSessionStart)
	hookEvent(t, a.SessionsDir, "s1", outside.Path, "UserPromptSubmit")

	report, err := a.Cycle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sessions != 1 {
		t.Errorf("report.Sessions = %d, want 1", report.Sessions)
	}

	got := a.Store.ReadSessions("laptop")
	if len(got) != 1 {
		t.Fatalf("published %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.Machine != "laptop" {
		t.Errorf("machine = %q", s.Machine)
	}
	if len(s.Repos) != 2 {
		t.Fatalf("repos = %+v", s.Repos)
	}
	scanned, ok := findRepo(mustRepos(t, a), "poppy")
	if !ok {
		t.Fatal("scan did not record poppy")
	}
	if s.Repos[0].RepoID == "" || s.Repos[0].RepoID != scanned.RepoID {
		t.Errorf("in-root RepoID = %q, want the scan's %q", s.Repos[0].RepoID, scanned.RepoID)
	}
	if s.Repos[1].RepoID == "" || s.Repos[1].RepoID == scanned.RepoID {
		t.Errorf("out-of-root RepoID = %q", s.Repos[1].RepoID)
	}
}

// Between scans the agent publishes only when a hook has marked a change.
func TestSessionsPublishBetweenScansOnlyWhenMarked(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := newRepoAt(t, root, "poppy")
	repo.Commit("init", "a.txt", "one\n")

	a := agentFor(t, "laptop", root, filepath.Join(t.TempDir(), "hub"), "")
	a.SessionsDir = t.TempDir()
	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if a.sessionsChanged() {
		t.Fatal("an empty store reads as changed after a cycle")
	}

	// The marker's resolution is the filesystem's; step past it.
	time.Sleep(10 * time.Millisecond)
	hookEvent(t, a.SessionsDir, "s1", repo.Path, sessions.EventSessionStart)
	if !a.sessionsChanged() {
		t.Fatal("a new session did not read as changed")
	}
	a.publishChangedSessions(ctx)
	if got := a.Store.ReadSessions("laptop"); len(got) != 1 {
		t.Fatalf("published %d sessions, want 1", len(got))
	}
	if a.sessionsChanged() {
		t.Error("still changed after publishing")
	}

	hookEvent(t, a.SessionsDir, "s1", repo.Path, "Stop")
	if a.sessionsChanged() {
		t.Error("a Stop with nothing new read as changed")
	}
}

func mustRepos(t *testing.T, a *Agent) []model.Repo {
	t.Helper()
	var out []model.Repo
	for _, r := range a.Store.ReadMachine(a.Config.Machine) {
		out = append(out, r)
	}
	return out
}
