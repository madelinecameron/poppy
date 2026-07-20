package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mads/poppy/internal/gittest"
	"github.com/mads/poppy/internal/model"
)

func manifestFor(machine string, repos int) model.Manifest {
	return model.Manifest{
		Schema:    model.SchemaVersion,
		Machine:   machine,
		OS:        "linux",
		Arch:      "arm64",
		Version:   "test",
		RepoCount: repos,
		Heartbeat: time.Now().UTC(),
	}
}

func repoFor(machine, name string, dirty int) model.Repo {
	r := model.Repo{
		Schema:  model.SchemaVersion,
		RepoID:  "repo-" + name,
		PathID:  machine + "-" + name,
		Machine: machine,
		Name:    name,
		Path:    "/code/" + name,
		Head:    model.Head{Branch: "main"},
	}
	if dirty > 0 {
		now := time.Now().UTC()
		r.Dirty = model.Dirty{Unstaged: dirty}
		r.DirtySince = &now
	}
	return r
}

func openStore(t *testing.T, dir, remote string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir, remote)
	if err != nil {
		t.Fatalf("open store %s: %v", dir, err)
	}
	return s
}

func machineNames(f model.Fleet) []string {
	var out []string
	for _, m := range f.Machines {
		out = append(out, m.Manifest.Machine)
	}
	return out
}

func TestPublishAndReadRoundTripLocalOnly(t *testing.T) {
	s := openStore(t, filepath.Join(t.TempDir(), "hub"), "")

	repos := []model.Repo{repoFor("laptop", "poppy", 3), repoFor("laptop", "api", 0)}
	if _, err := s.Publish(context.Background(), manifestFor("laptop", len(repos)), repos); err != nil {
		t.Fatalf("publish: %v", err)
	}

	fleet, err := s.Fleet()
	if err != nil {
		t.Fatalf("fleet: %v", err)
	}
	if len(fleet.Machines) != 1 {
		t.Fatalf("machines = %v, want [laptop]", machineNames(fleet))
	}
	if got := len(fleet.Machines[0].Repos); got != 2 {
		t.Errorf("repos = %d, want 2", got)
	}
}

// Each machine owns its own directory, so two machines publishing to one hub
// must both survive rather than the later push replacing the earlier state.
func TestTwoMachinesConverge(t *testing.T) {
	ctx := context.Background()
	remote := gittest.NewBare(t, Branch)
	root := t.TempDir()

	laptop := openStore(t, filepath.Join(root, "laptop"), remote)
	devbox := openStore(t, filepath.Join(root, "devbox"), remote)

	if _, err := laptop.Publish(ctx, manifestFor("laptop", 1), []model.Repo{repoFor("laptop", "poppy", 2)}); err != nil {
		t.Fatalf("laptop publish: %v", err)
	}
	if _, err := devbox.Publish(ctx, manifestFor("devbox", 1), []model.Repo{repoFor("devbox", "api", 5)}); err != nil {
		t.Fatalf("devbox publish: %v", err)
	}

	if err := laptop.Pull(ctx); err != nil {
		t.Fatalf("laptop pull: %v", err)
	}
	fleet, err := laptop.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	if got := machineNames(fleet); len(got) != 2 {
		t.Fatalf("machines = %v, want laptop and devbox", got)
	}
	for _, m := range fleet.Machines {
		if len(m.Repos) != 1 {
			t.Errorf("%s has %d repos, want 1", m.Manifest.Machine, len(m.Repos))
		}
	}
}

// A machine that publishes while the hub is unreachable must keep its state
// locally and converge with the fleet once the hub returns, without losing
// updates that other machines published in the meantime.
func TestOfflineMachineConvergesOnReconnect(t *testing.T) {
	ctx := context.Background()
	remote := gittest.NewBare(t, Branch)
	root := t.TempDir()
	laptopDir := filepath.Join(root, "laptop")

	laptop := openStore(t, laptopDir, remote)
	if _, err := laptop.Publish(ctx, manifestFor("laptop", 1), []model.Repo{repoFor("laptop", "poppy", 1)}); err != nil {
		t.Fatalf("initial publish: %v", err)
	}

	offline := openStore(t, laptopDir, filepath.Join(root, "gone"))
	pushed, _ := offline.Publish(ctx, manifestFor("laptop", 1), []model.Repo{repoFor("laptop", "poppy", 9)})
	if pushed {
		t.Fatal("publish reported success against an unreachable hub")
	}
	fleet, err := offline.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Machines) != 1 || fleet.Machines[0].Repos[0].Dirty.Unstaged != 9 {
		t.Errorf("offline state not retained locally: %+v", fleet.Machines)
	}

	devbox := openStore(t, filepath.Join(root, "devbox"), remote)
	if _, err := devbox.Publish(ctx, manifestFor("devbox", 1), []model.Repo{repoFor("devbox", "api", 4)}); err != nil {
		t.Fatalf("devbox publish: %v", err)
	}

	reconnected := openStore(t, laptopDir, remote)
	pushed, err = reconnected.Publish(ctx, manifestFor("laptop", 1), []model.Repo{repoFor("laptop", "poppy", 9)})
	if err != nil {
		t.Fatalf("reconnect publish: %v", err)
	}
	if !pushed {
		t.Fatal("reconnect publish did not reach the hub")
	}

	fleet, err = reconnected.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	if got := machineNames(fleet); len(got) != 2 {
		t.Fatalf("machines = %v, want laptop and devbox", got)
	}
	for _, m := range fleet.Machines {
		if m.Manifest.Machine == "laptop" && m.Repos[0].Dirty.Unstaged != 9 {
			t.Errorf("laptop state lost on reconnect: %+v", m.Repos[0].Dirty)
		}
		if m.Manifest.Machine == "devbox" && m.Repos[0].Dirty.Unstaged != 4 {
			t.Errorf("devbox state lost: %+v", m.Repos[0].Dirty)
		}
	}
}

// A repository that is deleted or moved must disappear from the fleet view
// rather than lingering as a phantom entry.
func TestPublishRemovesVanishedRepos(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, filepath.Join(t.TempDir(), "hub"), "")

	both := []model.Repo{repoFor("laptop", "poppy", 0), repoFor("laptop", "api", 0)}
	if _, err := s.Publish(ctx, manifestFor("laptop", 2), both); err != nil {
		t.Fatal(err)
	}
	one := []model.Repo{repoFor("laptop", "poppy", 0)}
	if _, err := s.Publish(ctx, manifestFor("laptop", 1), one); err != nil {
		t.Fatal(err)
	}

	fleet, err := s.Fleet()
	if err != nil {
		t.Fatal(err)
	}
	repos := fleet.Machines[0].Repos
	if len(repos) != 1 || repos[0].Name != "poppy" {
		t.Errorf("repos = %+v, want only poppy", repos)
	}
}

func TestNextSeqAdvancesPerMachine(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, filepath.Join(t.TempDir(), "hub"), "")

	if got := s.NextSeq("laptop"); got != 1 {
		t.Errorf("first seq = %d, want 1", got)
	}
	m := manifestFor("laptop", 0)
	m.Seq = s.NextSeq("laptop")
	if _, err := s.Publish(ctx, m, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.NextSeq("laptop"); got != 2 {
		t.Errorf("second seq = %d, want 2", got)
	}
	if got := s.NextSeq("devbox"); got != 1 {
		t.Errorf("unknown machine seq = %d, want 1", got)
	}
}

func TestReadMachineReturnsPreviousStateByPathID(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, filepath.Join(t.TempDir(), "hub"), "")

	r := repoFor("laptop", "poppy", 4)
	if _, err := s.Publish(ctx, manifestFor("laptop", 1), []model.Repo{r}); err != nil {
		t.Fatal(err)
	}

	prev := s.ReadMachine("laptop")
	got, ok := prev[r.PathID]
	if !ok {
		t.Fatalf("path id %q missing from %v", r.PathID, prev)
	}
	if got.Dirty.Unstaged != 4 {
		t.Errorf("dirty = %+v, want 4 unstaged", got.Dirty)
	}
	if len(s.ReadMachine("nonexistent")) != 0 {
		t.Error("unknown machine returned state")
	}
}

func TestOpenSucceedsAgainstEmptyRemote(t *testing.T) {
	ctx := context.Background()
	remote := gittest.NewBare(t, Branch)

	s := openStore(t, filepath.Join(t.TempDir(), "hub"), remote)
	pushed, err := s.Publish(ctx, manifestFor("laptop", 0), nil)
	if err != nil {
		t.Fatalf("publish to fresh hub: %v", err)
	}
	if !pushed {
		t.Error("first publish to an empty hub did not reach it")
	}
}

func TestFleetOnEmptyStore(t *testing.T) {
	s := openStore(t, filepath.Join(t.TempDir(), "hub"), "")
	fleet, err := s.Fleet()
	if err != nil {
		t.Fatalf("fleet on empty store: %v", err)
	}
	if len(fleet.Machines) != 0 {
		t.Errorf("machines = %v, want none", machineNames(fleet))
	}
}
