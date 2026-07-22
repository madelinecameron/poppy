package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/hub"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/snapshot"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newRepoAt(t *testing.T, root, name string) *gittest.Repo {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &gittest.Repo{T: t, Path: path}
	r.Git("init", "-q", "-b", "main", ".")
	r.Git("config", "user.name", "test")
	r.Git("config", "user.email", "test@example.com")
	r.Git("config", "commit.gpgsign", "false")
	return r
}

func agentFor(t *testing.T, machine, codeRoot, hubDir, remote string) *Agent {
	t.Helper()
	store, err := hub.Open(context.Background(), hubDir, remote)
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	cfg := config.Config{
		Machine:             machine,
		Roots:               []string{codeRoot},
		HubRemote:           remote,
		ScanIntervalSeconds: 300,
		MaxDepth:            8,
		StaleAfterSeconds:   3600,
		Snapshot:            config.Snapshot{Enabled: true, MaxFileBytes: 1 << 20},
	}
	return New(cfg, store, quietLogger())
}

func findRepo(repos []model.Repo, name string) (model.Repo, bool) {
	for _, r := range repos {
		if r.Name == name {
			return r, true
		}
	}
	return model.Repo{}, false
}

func TestCycleRecordsFleetState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	clean := newRepoAt(t, root, "clean-project")
	clean.Commit("init", "a.txt", "one\n")

	dirty := newRepoAt(t, root, "dirty-project")
	dirty.Commit("init", "a.txt", "one\n")
	dirty.Write("wip.txt", "unfinished\n")

	a := agentFor(t, "laptop", root, filepath.Join(t.TempDir(), "hub"), "")

	report, err := a.Cycle(ctx)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if report.Repos != 2 {
		t.Errorf("repos = %d, want 2", report.Repos)
	}
	if report.Dirty != 1 {
		t.Errorf("dirty = %d, want 1", report.Dirty)
	}

	fleet, err := a.Store.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Machines) != 1 {
		t.Fatalf("machines = %d, want 1", len(fleet.Machines))
	}
	m := fleet.Machines[0]
	if m.Manifest.Machine != "laptop" || m.Manifest.RepoCount != 2 {
		t.Errorf("manifest = %+v", m.Manifest)
	}
	if m.Manifest.Heartbeat.IsZero() {
		t.Error("heartbeat not recorded")
	}

	got, ok := findRepo(m.Repos, "dirty-project")
	if !ok {
		t.Fatalf("dirty-project missing from %+v", m.Repos)
	}
	if got.Dirty.Untracked != 1 || got.DirtySince == nil {
		t.Errorf("dirty state = %+v since %v", got.Dirty, got.DirtySince)
	}
}

// Dirty ages drive the whole product, so they must survive repeated cycles.
func TestCyclePreservesDirtyAgeAcrossScans(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := newRepoAt(t, root, "project")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("wip.txt", "work\n")

	a := agentFor(t, "laptop", root, filepath.Join(t.TempDir(), "hub"), "")

	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := a.Store.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	firstRepo, _ := findRepo(first.Machines[0].Repos, "project")

	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := a.Store.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	secondRepo, _ := findRepo(second.Machines[0].Repos, "project")

	if firstRepo.DirtySince == nil || secondRepo.DirtySince == nil {
		t.Fatal("DirtySince missing")
	}
	if !firstRepo.DirtySince.Equal(*secondRepo.DirtySince) {
		t.Errorf("DirtySince moved between scans: %v -> %v", firstRepo.DirtySince, secondRepo.DirtySince)
	}
	if secondRepo.Seq <= firstRepo.Seq {
		t.Errorf("seq did not advance: %d -> %d", firstRepo.Seq, secondRepo.Seq)
	}
}

// The core promise: work on one machine is visible from another.
func TestTwoAgentsShareStateThroughHub(t *testing.T) {
	ctx := context.Background()
	remote := gittest.NewBare(t, hub.Branch)
	stores := t.TempDir()

	devboxCode := t.TempDir()
	devboxRepo := newRepoAt(t, devboxCode, "poppy")
	devboxRepo.Commit("init", "a.txt", "one\n")
	devboxRepo.Write("secret-work.txt", "unfinished\n")

	devbox := agentFor(t, "devbox", devboxCode, filepath.Join(stores, "devbox"), remote)
	if _, err := devbox.Cycle(ctx); err != nil {
		t.Fatalf("devbox cycle: %v", err)
	}

	laptopCode := t.TempDir()
	laptop := agentFor(t, "laptop", laptopCode, filepath.Join(stores, "laptop"), remote)
	if _, err := laptop.Cycle(ctx); err != nil {
		t.Fatalf("laptop cycle: %v", err)
	}

	fleet, err := laptop.Store.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Machines) != 2 {
		t.Fatalf("laptop sees %d machines, want 2", len(fleet.Machines))
	}

	var found bool
	for _, m := range fleet.Machines {
		if m.Manifest.Machine != "devbox" {
			continue
		}
		r, ok := findRepo(m.Repos, "poppy")
		if !ok {
			t.Fatalf("devbox poppy repo not visible: %+v", m.Repos)
		}
		if !r.Dirty.Any() {
			t.Error("devbox dirty tree not visible from laptop")
		}
		found = true
	}
	if !found {
		t.Error("devbox not visible from laptop")
	}
}

// State documents record paths but never file contents; the hub must not
// become an accidental copy of the working tree.
func TestPublishedStateExcludesFileContents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := newRepoAt(t, root, "project")
	repo.Commit("init", "a.txt", "one\n")

	const secret = "SUPER_SECRET_TOKEN_VALUE"
	repo.Write("config.env", secret+"\n")

	hubDir := filepath.Join(t.TempDir(), "hub")
	a := agentFor(t, "laptop", root, hubDir, "")
	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}

	var leaked bool
	err := filepath.Walk(hubDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && len(data) > 0 {
			if containsBytes(data, secret) {
				leaked = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Error("file contents leaked into the hub")
	}

	fleet, _ := a.Store.Fleet()
	r, _ := findRepo(fleet.Machines[0].Repos, "project")
	var listed bool
	for _, f := range r.Dirty.Files {
		if f == "config.env" {
			listed = true
		}
	}
	if !listed {
		t.Errorf("changed path not recorded: %v", r.Dirty.Files)
	}
}

// A scan captures every dirty tree it finds, so simply running the daemon
// protects uncommitted work without anyone remembering to ask.
func TestCycleCapturesDirtyTrees(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	dirty := newRepoAt(t, root, "dirty-project")
	dirty.Commit("init", "a.txt", "one\n")
	dirty.Write("wip.txt", "unfinished\n")

	clean := newRepoAt(t, root, "clean-project")
	clean.Commit("init", "a.txt", "one\n")

	a := agentFor(t, "laptop", root, filepath.Join(t.TempDir(), "hub"), "")

	report, err := a.Cycle(ctx)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if report.Snapshots != 1 {
		t.Errorf("snapshots = %d, want 1 (only the dirty repo)", report.Snapshots)
	}

	snaps, err := snapshot.List(ctx, dirty.Path, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("dirty repo snapshots = %d, want 1", len(snaps))
	}

	if snaps, err := snapshot.List(ctx, clean.Path, "laptop", ""); err != nil || len(snaps) != 0 {
		t.Errorf("clean repo snapshots = %v (err %v), want none", snaps, err)
	}

	// A second cycle over an unchanged tree must not pile up duplicates.
	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := snapshot.List(ctx, dirty.Path, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Errorf("snapshots after second cycle = %d, want 1", len(after))
	}
}

func TestCycleSkipsCaptureWhenDisabled(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := newRepoAt(t, root, "project")
	repo.Commit("init", "a.txt", "one\n")
	repo.Write("wip.txt", "work\n")

	a := agentFor(t, "laptop", root, filepath.Join(t.TempDir(), "hub"), "")
	a.Config.Snapshot.Enabled = false

	report, err := a.Cycle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Snapshots != 0 {
		t.Errorf("snapshots = %d, want 0 when disabled", report.Snapshots)
	}
	if snaps, _ := snapshot.List(ctx, repo.Path, "laptop", ""); len(snaps) != 0 {
		t.Errorf("captured %d snapshots despite being disabled", len(snaps))
	}
}

func TestCycleHonoursSnapshotExclusions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	secret := newRepoAt(t, root, "client-secret")
	secret.Commit("init", "a.txt", "one\n")
	secret.Write("wip.txt", "sensitive work\n")

	a := agentFor(t, "laptop", root, filepath.Join(t.TempDir(), "hub"), "")
	a.Config.Snapshot.ExcludeRepos = []string{"client-*"}

	if _, err := a.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := snapshot.List(ctx, secret.Path, "laptop", ""); len(snaps) != 0 {
		t.Errorf("excluded repository was captured (%d snapshots)", len(snaps))
	}
}

func containsBytes(haystack []byte, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(string(haystack), needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
