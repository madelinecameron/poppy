package identity

import "testing"

// The same project cloned over ssh on one machine and https on another must
// resolve to one id, or the fleet view splits a single project in two.
func TestCanonicalRemoteUnifiesURLForms(t *testing.T) {
	forms := []string{
		"git@github.com:mads/poppy.git",
		"https://github.com/mads/poppy.git",
		"https://github.com/mads/poppy",
		"ssh://git@github.com/mads/poppy.git",
		"git@GitHub.com:mads/poppy.git",
		"https://mads@github.com/mads/poppy.git",
		"git@github.com:mads/poppy.git/",
	}
	want := CanonicalRemote(forms[0])
	if want != "github.com/mads/poppy" {
		t.Fatalf("canonical = %q, want github.com/mads/poppy", want)
	}
	for _, f := range forms {
		if got := CanonicalRemote(f); got != want {
			t.Errorf("CanonicalRemote(%q) = %q, want %q", f, got, want)
		}
	}
}

func TestCanonicalRemoteDistinguishesDifferentRepos(t *testing.T) {
	a := CanonicalRemote("git@github.com:mads/poppy.git")
	b := CanonicalRemote("git@github.com:mads/other.git")
	c := CanonicalRemote("git@gitlab.com:mads/poppy.git")
	if a == b || a == c || b == c {
		t.Errorf("distinct repos collided: %q %q %q", a, b, c)
	}
}

func TestCanonicalRemoteDropsPort(t *testing.T) {
	a := CanonicalRemote("ssh://git@example.com:2222/mads/poppy.git")
	b := CanonicalRemote("ssh://git@example.com/mads/poppy.git")
	if a != b {
		t.Errorf("port changed identity: %q vs %q", a, b)
	}
}

func TestRepoIDStableAcrossPathsWithSameRemote(t *testing.T) {
	remote := "git@github.com:mads/poppy.git"
	a := RepoID(remote, "/home/mads/code/poppy")
	b := RepoID(remote, "/Users/mads/code/personal/poppy")
	if a != b {
		t.Errorf("RepoID differs by path: %q vs %q", a, b)
	}
}

func TestRepoIDFallsBackToPathWithoutRemote(t *testing.T) {
	a := RepoID("", "/home/mads/code/scratch")
	b := RepoID("", "/home/mads/code/other")
	if a == b {
		t.Error("remoteless repos at different paths collided")
	}
	if a == "" || len(a) != IDLen {
		t.Errorf("id = %q, want %d hex chars", a, IDLen)
	}
}

// Worktrees and second clones of one project share a RepoID, so state
// documents must be keyed by path to avoid overwriting each other.
func TestPathIDDistinguishesWorktreesOfSameRepo(t *testing.T) {
	a := PathID("laptop", "/code/poppy")
	b := PathID("laptop", "/code/poppy-wt-fix")
	if a == b {
		t.Error("worktree and main checkout collided")
	}
	if PathID("laptop", "/code/poppy") == PathID("devbox", "/code/poppy") {
		t.Error("same path on different machines collided")
	}
}
