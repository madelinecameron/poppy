package snapshot

import (
	"context"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/gittest"
)

func at(base time.Time, d time.Duration) time.Time { return base.Add(-d) }

func TestRetainKeepsEverythingInsideTheFullWindow(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	p := DefaultPolicy()

	snaps := []Snapshot{
		{SHA: "a", Branch: "main", Taken: at(now, time.Hour)},
		{SHA: "b", Branch: "main", Taken: at(now, 6*time.Hour)},
		{SHA: "c", Branch: "main", Taken: at(now, 3*24*time.Hour)},
		{SHA: "d", Branch: "main", Taken: at(now, 6*24*time.Hour)},
	}

	keep, drop := p.Retain(snaps, now)
	if len(keep) != 4 || len(drop) != 0 {
		t.Errorf("keep=%d drop=%d, want all four kept", len(keep), len(drop))
	}
}

func TestRetainThinsToOnePerDayInTheDailyWindow(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	p := DefaultPolicy()

	// Three snapshots on one day, well outside the keep-all window.
	day := 30 * 24 * time.Hour
	snaps := []Snapshot{
		{SHA: "newest", Branch: "main", Taken: at(now, day)},
		{SHA: "middle", Branch: "main", Taken: at(now, day+2*time.Hour)},
		{SHA: "oldest", Branch: "main", Taken: at(now, day+5*time.Hour)},
	}

	keep, drop := p.Retain(snaps, now)
	// The first is retained as the branch's newest, and it is also that day's
	// survivor, so the other two go.
	if len(keep) != 1 || keep[0].SHA != "newest" {
		t.Errorf("keep = %+v, want only the newest of the day", keep)
	}
	if len(drop) != 2 {
		t.Errorf("drop = %d, want 2", len(drop))
	}
}

func TestRetainDropsBeyondTheDailyWindow(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	p := DefaultPolicy()

	snaps := []Snapshot{
		{SHA: "current", Branch: "main", Taken: at(now, time.Hour)},
		{SHA: "ancient", Branch: "main", Taken: at(now, 200*24*time.Hour)},
	}

	keep, drop := p.Retain(snaps, now)
	if len(drop) != 1 || drop[0].SHA != "ancient" {
		t.Errorf("drop = %+v, want the ancient snapshot", drop)
	}
	if len(keep) != 1 {
		t.Errorf("keep = %+v", keep)
	}
}

// However stale, the newest snapshot of a branch is the only record of that
// work. Pruning it would delete the very thing poppy exists to protect.
func TestRetainAlwaysKeepsNewestPerBranch(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	p := DefaultPolicy()

	snaps := []Snapshot{
		{SHA: "main-old", Machine: "devbox", Branch: "main", Taken: at(now, 300*24*time.Hour)},
		{SHA: "feat-old", Machine: "devbox", Branch: "feature", Taken: at(now, 400*24*time.Hour)},
	}

	keep, drop := p.Retain(snaps, now)
	if len(keep) != 2 {
		t.Errorf("keep = %+v, want the newest of each branch", keep)
	}
	if len(drop) != 0 {
		t.Errorf("drop = %+v, want nothing", drop)
	}
}

func TestRetainTreatsBranchesIndependently(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	p := DefaultPolicy()
	day := 30 * 24 * time.Hour

	snaps := []Snapshot{
		{SHA: "main-1", Machine: "laptop", Branch: "main", Taken: at(now, day)},
		{SHA: "main-2", Machine: "laptop", Branch: "main", Taken: at(now, day+time.Hour)},
		{SHA: "feat-1", Machine: "laptop", Branch: "feature", Taken: at(now, day)},
		{SHA: "feat-2", Machine: "laptop", Branch: "feature", Taken: at(now, day+time.Hour)},
	}

	keep, _ := p.Retain(snaps, now)
	branches := map[string]int{}
	for _, s := range keep {
		branches[s.Branch]++
	}
	if branches["main"] != 1 || branches["feature"] != 1 {
		t.Errorf("kept %v, want one per branch", branches)
	}
}

func TestPruneDeletesRefs(t *testing.T) {
	ctx := context.Background()
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	s := New("laptop")
	base := time.Now()

	// Three captures spread across time, each a real change so none is skipped.
	for i, age := range []time.Duration{200 * 24 * time.Hour, 100 * 24 * time.Hour, time.Hour} {
		taken := base.Add(-age)
		s.Now = func() time.Time { return taken }
		repo.Write("wip.txt", string(rune('a'+i))+"\n")
		if _, err := s.Create(ctx, repo.Path); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	before, err := List(ctx, repo.Path, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 {
		t.Fatalf("snapshots = %d, want 3", len(before))
	}

	dropped, err := Prune(ctx, repo.Path, "laptop", DefaultPolicy(), base)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}

	after, err := List(ctx, repo.Path, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("remaining = %d, want 1", len(after))
	}
	if after[0].Taken.Before(base.Add(-24 * time.Hour)) {
		t.Errorf("prune kept the wrong snapshot: %v", after[0].Taken)
	}
}

func TestPruneOnEmptyRepositoryIsHarmless(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	dropped, err := Prune(context.Background(), repo.Path, "laptop", DefaultPolicy(), time.Now())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
}
