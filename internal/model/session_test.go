package model

import (
	"testing"
	"time"
)

func sessionFleet(now time.Time) Fleet {
	ended := now.Add(-5 * time.Minute)
	return Fleet{Machines: []MachineState{
		{
			Manifest: Manifest{Machine: "laptop"},
			Sessions: []Session{
				{
					Machine: "laptop", Harness: "claude", SessionID: "a", LastSeen: now.Add(-2 * time.Minute),
					Repos: []SessionRepo{
						{Name: "poppy", RepoID: "id-poppy", Branch: "feat/sessions"},
						{Name: "mailbox", RepoID: "id-mailbox", Branch: "main"},
					},
				},
				{
					Machine: "laptop", Harness: "claude", SessionID: "ended", LastSeen: now.Add(-time.Minute), Ended: &ended,
					Repos: []SessionRepo{{Name: "poppy", RepoID: "id-poppy", Branch: "main"}},
				},
			},
		},
		{
			Manifest: Manifest{Machine: "devbox"},
			Sessions: []Session{
				{
					Machine: "devbox", Harness: "codex", SessionID: "b", LastSeen: now.Add(-3 * time.Hour),
					Repos: []SessionRepo{{Name: "Poppy", RepoID: "id-poppy", Branch: "feat/sessions"}},
				},
			},
		},
	}}
}

func ids(list []Session) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.SessionID)
	}
	return out
}

func TestFleetSessionsFiltersAndOrders(t *testing.T) {
	now := time.Now()
	f := sessionFleet(now)

	for name, tc := range map[string]struct {
		filter SessionFilter
		want   []string
	}{
		"everything, most recent first": {SessionFilter{}, []string{"ended", "a", "b"}},
		"repo by name, any case":        {SessionFilter{Repo: "POPPY"}, []string{"ended", "a", "b"}},
		"repo by id":                    {SessionFilter{Repo: "id-mailbox"}, []string{"a"}},
		"branch":                        {SessionFilter{Branch: "feat/sessions"}, []string{"a", "b"}},
		"repo and branch on one entry":  {SessionFilter{Repo: "mailbox", Branch: "feat/sessions"}, nil},
		"repo and branch together":      {SessionFilter{Repo: "poppy", Branch: "main"}, []string{"ended"}},
		"machine":                       {SessionFilter{Machine: "devbox"}, []string{"b"}},
		"active excludes ended and old": {SessionFilter{ActiveWithin: 30 * time.Minute, Now: now}, []string{"a"}},
		"wider window":                  {SessionFilter{ActiveWithin: 4 * time.Hour, Now: now}, []string{"a", "b"}},
	} {
		got := ids(f.Sessions(tc.filter))
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v, want %v", name, got, tc.want)
				break
			}
		}
	}
}
