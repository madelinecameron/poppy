package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/model"
)

func TestStatusJSONCarriesPoppysJudgements(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	remotes := map[string]string{"origin": "git@github.com:x/y.git"}
	fleet := model.Fleet{Machines: []model.MachineState{
		{
			Manifest: model.Manifest{Machine: "mac", Heartbeat: now},
			Repos: []model.Repo{
				{Name: "clean", Remotes: remotes, Branches: []model.Branch{{Name: "main", SHA: "a", Upstream: "origin/main"}}},
				{Name: "ahead", Remotes: remotes, Branches: []model.Branch{
					{Name: "main", SHA: "a", Upstream: "origin/main", Ahead: 2},
					{Name: "wip", SHA: "b"},
				}},
				{Name: "local", Head: model.Head{Branch: "main", SHA: "c"}},
			},
		},
		{Manifest: model.Manifest{Machine: "gone", Heartbeat: now.Add(-48 * time.Hour)}},
	}}

	data, err := json.Marshal(statusView(fleet, now, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Machines []struct {
			Manifest struct {
				Machine string `json:"machine"`
			} `json:"Manifest"`
			Stale    bool `json:"stale"`
			AtRisk   int  `json:"atRisk"`
			Unpushed int  `json:"unpushed"`
			Repos    []struct {
				Name     string `json:"name"`
				AtRisk   bool   `json:"atRisk"`
				Unpushed int    `json:"unpushed"`
			} `json:"Repos"`
		} `json:"Machines"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Machines) != 2 {
		t.Fatalf("machines = %d, want 2: %s", len(doc.Machines), data)
	}

	mac := doc.Machines[0]
	if mac.Manifest.Machine != "mac" {
		t.Errorf("manifest not carried: %s", data)
	}
	if mac.Stale || mac.AtRisk != 2 || mac.Unpushed != 1 {
		t.Errorf("mac stale=%v atRisk=%d unpushed=%d, want false 2 1", mac.Stale, mac.AtRisk, mac.Unpushed)
	}
	want := map[string]struct {
		atRisk   bool
		unpushed int
	}{"clean": {false, 0}, "ahead": {true, 2}, "local": {true, 0}}
	for _, r := range mac.Repos {
		w := want[r.Name]
		if r.AtRisk != w.atRisk || r.Unpushed != w.unpushed {
			t.Errorf("%s atRisk=%v unpushed=%d, want %v %d", r.Name, r.AtRisk, r.Unpushed, w.atRisk, w.unpushed)
		}
	}
	if !doc.Machines[1].Stale {
		t.Error("a machine silent for two days is not stale")
	}
}
