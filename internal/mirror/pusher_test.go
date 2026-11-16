package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/snapshot"
)

// countSSH installs an ssh in front of fakeSSH that records each invocation,
// and returns a function reporting how many sessions were opened so far.
func countSSH(t *testing.T) func() int {
	t.Helper()
	home := gitShellHost(t)
	log := filepath.Join(home, "ssh.log")
	script := "#!/bin/sh\necho \"$*\" >> " + shellQuote(log) + "\n" + strings.TrimPrefix(fakeSSH, "#!/bin/sh\n")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		data, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestPusherSkipsUnchangedRefs(t *testing.T) {
	ctx := context.Background()
	sessions := countSSH(t)
	repo, _ := dirtyRepoWithSnapshot(t, "devbox")
	m := For(Config{Host: "fleet@hub", Dir: filepath.Join(os.Getenv("HOME"), "poppy-mirrors")}, "proj")
	p := NewPusher()

	if pushed, err := p.Push(ctx, m, repo.Path, "devbox"); err != nil || !pushed {
		t.Fatalf("first push: pushed=%v err=%v", pushed, err)
	}
	if got := sessions(); got != 2 {
		t.Fatalf("first push opened %d sessions, want 2 (init and push)", got)
	}

	if pushed, err := p.Push(ctx, m, repo.Path, "devbox"); err != nil || pushed {
		t.Fatalf("unchanged push: pushed=%v err=%v, want a skip", pushed, err)
	}
	if got := sessions(); got != 2 {
		t.Fatalf("unchanged push opened %d new sessions, want none", got-2)
	}

	repo.Write("tracked.txt", "more wip\n")
	snap, err := snapshot.New("devbox").Create(ctx, repo.Path)
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if pushed, err := p.Push(ctx, m, repo.Path, "devbox"); err != nil || !pushed {
		t.Fatalf("changed push: pushed=%v err=%v", pushed, err)
	}
	if got := sessions(); got != 3 {
		t.Fatalf("changed push opened %d new sessions, want 1 (no init)", got-2)
	}

	other, _ := dirtyRepoWithSnapshot(t, "laptop")
	if err := m.FetchMachine(ctx, other.Path, "devbox"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, err := snapshot.List(ctx, other.Path, "devbox", "")
	if err != nil || len(got) == 0 || got[0].SHA != snap.SHA {
		t.Fatalf("mirror holds %v (err %v), want newest %s", got, err, snap.SHA)
	}
}

func TestPusherRefreshesAndRecreatesAMissingMirror(t *testing.T) {
	ctx := context.Background()
	sessions := countSSH(t)
	repo, snap := dirtyRepoWithSnapshot(t, "devbox")
	dir := filepath.Join(os.Getenv("HOME"), "poppy-mirrors")
	m := For(Config{Host: "fleet@hub", Dir: dir}, "proj")

	now := time.Unix(1_700_000_000, 0)
	p := NewPusher()
	p.now = func() time.Time { return now }

	if _, err := p.Push(ctx, m, repo.Path, "devbox"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "proj.git")); err != nil {
		t.Fatal(err)
	}

	now = now.Add(p.Refresh)
	if _, err := p.Push(ctx, m, repo.Path, "devbox"); err == nil {
		t.Fatal("refresh push to a removed mirror succeeded, want an error")
	}
	before := sessions()
	if pushed, err := p.Push(ctx, m, repo.Path, "devbox"); err != nil || !pushed {
		t.Fatalf("push after failure: pushed=%v err=%v", pushed, err)
	}
	if got := sessions() - before; got != 2 {
		t.Fatalf("push after failure opened %d sessions, want 2 (init and push)", got)
	}

	other, _ := dirtyRepoWithSnapshot(t, "laptop")
	if err := m.FetchMachine(ctx, other.Path, "devbox"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got, err := snapshot.List(ctx, other.Path, "devbox", ""); err != nil || len(got) != 1 || got[0].SHA != snap.SHA {
		t.Fatalf("recreated mirror holds %v (err %v), want %s", got, err, snap.SHA)
	}
}
