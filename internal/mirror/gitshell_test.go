package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madelinecameron/poppy/internal/snapshot"
)

// fakeSSH behaves like ssh to an account whose login shell is git-shell: it
// runs git's own transport commands and anything in ~/git-shell-commands, and
// refuses everything else.
const fakeSSH = `#!/bin/sh
for last; do :; done
eval "set -- $last"
case "$1" in
  git-receive-pack) shift; exec git receive-pack "$@" ;;
  git-upload-pack)  shift; exec git upload-pack "$@" ;;
  */*) echo "fatal: unrecognized command '$1'" >&2; exit 128 ;;
esac
cmd="$HOME/git-shell-commands/$1"
[ -x "$cmd" ] || { echo "fatal: unrecognized command '$1'" >&2; exit 128; }
shift
exec "$cmd" "$@"
`

// gitShellHost points ssh at fakeSSH and gives the mirror account a home with
// the shipped git-shell-commands installed. It returns that home.
func gitShellHost(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	home := t.TempDir()
	cmds := filepath.Join(home, "git-shell-commands")
	if err := os.MkdirAll(cmds, 0o755); err != nil {
		t.Fatal(err)
	}
	shipped, err := filepath.Glob("../../deploy/git-shell-commands/*")
	if err != nil || len(shipped) == 0 {
		t.Fatalf("no shipped git-shell-commands: %v", err)
	}
	for _, src := range shipped {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cmds, filepath.Base(src)), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	return home
}

func TestPushAndFetchThroughGitShellAccount(t *testing.T) {
	ctx := context.Background()
	repo, snap := dirtyRepoWithSnapshot(t, "devbox")
	home := gitShellHost(t)

	m := For(Config{Host: "fleet@hub", Dir: filepath.Join(home, "poppy-mirrors")}, "github.com/acme/proj")
	if err := m.Push(ctx, repo.Path, "devbox"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := m.Push(ctx, repo.Path, "devbox"); err != nil {
		t.Fatalf("second push to an existing mirror: %v", err)
	}

	other, _ := dirtyRepoWithSnapshot(t, "laptop")
	if err := m.FetchMachine(ctx, other.Path, "devbox"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, err := snapshot.List(ctx, other.Path, "devbox", "")
	if err != nil || len(got) != 1 || got[0].SHA != snap.SHA {
		t.Fatalf("fetched %v (err %v), want %s", got, err, snap.SHA)
	}
}

func TestMirrorInitRefusesPathsOutsideTheMirrorRoot(t *testing.T) {
	ctx := context.Background()
	home := gitShellHost(t)

	for _, dir := range []string{
		t.TempDir(),
		filepath.Join(home, "poppy-mirrors", "..", "elsewhere"),
	} {
		err := For(Config{Host: "fleet@hub", Dir: dir}, "proj").Ensure(ctx)
		if err == nil {
			t.Errorf("Ensure under %s succeeded, want refusal", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "elsewhere")); err == nil {
		t.Error("a mirror was created outside ~/poppy-mirrors")
	}
}

func TestGitShellAccountRunsNoShellCommands(t *testing.T) {
	gitShellHost(t)
	m := For(Config{Host: "fleet@hub", Dir: "/x"}, "proj")
	err := m.ssh(context.Background(), "mkdir -p /tmp/x && git init --bare /tmp/x")
	if err == nil || !strings.Contains(err.Error(), "unrecognized command") {
		t.Fatalf("shell command over git-shell: %v, want refusal", err)
	}
}
