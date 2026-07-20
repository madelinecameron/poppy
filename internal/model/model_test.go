package model

import "testing"

func repo(remotes map[string]string, branches ...Branch) Repo {
	return Repo{Remotes: remotes, Branches: branches, Head: Head{Branch: "main", SHA: "abc"}}
}

var origin = map[string]string{"origin": "git@github.com:mads/poppy.git"}

func TestUnpushedRequiresARemote(t *testing.T) {
	tracked := Branch{Name: "main", SHA: "abc", Upstream: "origin/main"}
	ahead := Branch{Name: "main", SHA: "abc", Upstream: "origin/main", Ahead: 2}
	noUpstream := Branch{Name: "wip", SHA: "def"}

	tests := []struct {
		name     string
		repo     Repo
		unpushed bool
	}{
		{"in sync", repo(origin, tracked), false},
		{"ahead of upstream", repo(origin, ahead), true},
		{"branch never pushed", repo(origin, tracked, noUpstream), true},
		// Without a remote every branch lacks an upstream. Reporting each one
		// as unpushed would bury the repositories that genuinely diverged.
		{"no remote at all", repo(nil, tracked, noUpstream), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.repo.Unpushed(); got != tt.unpushed {
				t.Errorf("Unpushed() = %v, want %v", got, tt.unpushed)
			}
		})
	}
}

func TestLocalOnlyFlagsRepositoriesWithNowhereToPush(t *testing.T) {
	withRemote := repo(origin, Branch{Name: "main", SHA: "abc", Upstream: "origin/main"})
	if withRemote.LocalOnly() {
		t.Error("repo with a remote reported as local-only")
	}

	noRemote := repo(nil, Branch{Name: "main", SHA: "abc"})
	if !noRemote.LocalOnly() {
		t.Error("repo without a remote not reported as local-only")
	}

	// A freshly initialised repository holds no work yet, so it is not at risk.
	empty := Repo{Head: Head{Unborn: true}}
	if empty.LocalOnly() {
		t.Error("unborn repo reported as local-only")
	}
}

func TestAtRiskCoversEveryWayWorkCanExistInOnePlace(t *testing.T) {
	safe := repo(origin, Branch{Name: "main", SHA: "abc", Upstream: "origin/main"})
	if safe.AtRisk() {
		t.Error("clean, pushed repo reported at risk")
	}

	dirty := safe
	dirty.Dirty = Dirty{Unstaged: 1}
	if !dirty.AtRisk() {
		t.Error("dirty tree not at risk")
	}

	stashed := safe
	stashed.Stashes = 1
	if !stashed.AtRisk() {
		t.Error("stash not at risk")
	}

	ahead := repo(origin, Branch{Name: "main", SHA: "abc", Upstream: "origin/main", Ahead: 1})
	if !ahead.AtRisk() {
		t.Error("unpushed commits not at risk")
	}

	noRemote := repo(nil, Branch{Name: "main", SHA: "abc"})
	if !noRemote.AtRisk() {
		t.Error("repo with no remote not at risk")
	}
}

func TestDirtyCounts(t *testing.T) {
	var empty Dirty
	if empty.Any() || empty.Total() != 0 {
		t.Errorf("empty dirty = %+v, want none", empty)
	}

	d := Dirty{Staged: 1, Unstaged: 2, Untracked: 3, Unmerged: 4}
	if !d.Any() {
		t.Error("Any() = false")
	}
	if got := d.Total(); got != 10 {
		t.Errorf("Total() = %d, want 10", got)
	}
}
