package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mads/poppy/internal/config"
	"github.com/mads/poppy/internal/model"
)

// fakeSource serves a fixed fleet, standing in for the hub store.
type fakeSource struct {
	fleet    model.Fleet
	pullErr  error
	pullSeen int
}

func (f *fakeSource) Fleet() (model.Fleet, error) { return f.fleet, nil }
func (f *fakeSource) Pull(context.Context) error  { f.pullSeen++; return f.pullErr }

func sampleFleet(now time.Time) model.Fleet {
	dirtySince := now.Add(-50 * time.Hour)
	oldSince := now.Add(-2 * time.Hour)
	return model.Fleet{Machines: []model.MachineState{
		{
			Manifest: model.Manifest{Machine: "devbox", OS: "linux", Arch: "amd64", Heartbeat: now.Add(-time.Minute)},
			Repos: []model.Repo{
				{
					RepoID: "p1", Name: "poppy", Machine: "devbox", Path: "/home/mads/code/poppy",
					Head:  model.Head{Branch: "feature/auth"},
					Dirty: model.Dirty{Unstaged: 3, Untracked: 1}, DirtySince: &dirtySince,
					Branches:   []model.Branch{{Name: "feature/auth", SHA: "aaa"}, {Name: "main", SHA: "bbb", Upstream: "origin/main"}},
					Remotes:    map[string]string{"origin": "git@github.com:mads/poppy.git"},
					LastCommit: &model.Commit{SHA: "aaa", Subject: "wip auth refactor", When: now.Add(-51 * time.Hour)},
					Snapshot:   &model.SnapshotRef{SHA: "snap111", Branch: "feature/auth", Taken: now.Add(-30 * time.Minute), Synced: true},
				},
				{
					RepoID: "p2", Name: "api", Machine: "devbox", Path: "/home/mads/code/api",
					Head:     model.Head{Branch: "main"},
					Branches: []model.Branch{{Name: "main", SHA: "ccc", Upstream: "origin/main", Ahead: 2}},
					Remotes:  map[string]string{"origin": "git@github.com:mads/api.git"},
				},
			},
		},
		{
			Manifest: model.Manifest{Machine: "laptop", OS: "darwin", Arch: "arm64", Heartbeat: now.Add(-90 * time.Minute)},
			Repos: []model.Repo{
				{
					RepoID: "p3", Name: "notes", Machine: "laptop", Path: "/Users/mads/code/notes",
					Head:  model.Head{Branch: "main"},
					Dirty: model.Dirty{Untracked: 2}, DirtySince: &oldSince,
				},
			},
		},
	}}
}

// exchange runs one request through the server and returns the decoded result.
func exchange(t *testing.T, s *Server, method string, params any) json.RawMessage {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		req["params"] = params
	}
	line, _ := json.Marshal(req)

	var out bytes.Buffer
	if err := s.Serve(context.Background(), bytes.NewReader(append(line, '\n')), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out.String(), err)
	}
	if resp.Error != nil {
		t.Fatalf("%s returned error: %+v", method, resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	return raw
}

// callTool invokes a tool and returns its text content, failing on tool error.
func callTool(t *testing.T, s *Server, name string, args map[string]any) string {
	t.Helper()
	raw := exchange(t, s, "tools/call", map[string]any{"name": name, "arguments": args})
	var res callToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode call result: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("%s returned no content", name)
	}
	if res.IsError {
		t.Fatalf("%s reported an error: %s", name, res.Content[0].Text)
	}
	return res.Content[0].Text
}

func newTestServer(now time.Time) (*Server, *fakeSource) {
	src := &fakeSource{fleet: sampleFleet(now)}
	cfg := config.Config{Machine: "laptop", StaleAfterSeconds: 3600}
	return New(cfg, src), src
}

func TestInitializeEchoesClientProtocolVersion(t *testing.T) {
	s, _ := newTestServer(time.Now())
	raw := exchange(t, s, "initialize", map[string]any{"protocolVersion": "2025-06-18"})

	var res initializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q, want the client's version echoed", res.ProtocolVersion)
	}
	if res.ServerInfo.Name != "poppy" {
		t.Errorf("serverInfo.name = %q", res.ServerInfo.Name)
	}
	if res.Capabilities.Tools == nil {
		t.Error("server did not advertise tools capability")
	}
}

func TestNotificationsGetNoResponse(t *testing.T) {
	s, _ := newTestServer(time.Now())
	line := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")

	var out bytes.Buffer
	if err := s.Serve(context.Background(), bytes.NewReader(line), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("notification produced a response: %q", out.String())
	}
}

func TestToolsListReturnsEverySchema(t *testing.T) {
	s, _ := newTestServer(time.Now())
	raw := exchange(t, s, "tools/list", nil)

	var res toolsListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"fleet_status": true, "dirty_trees": true, "find_work": true,
		"repo_state": true, "snapshot_history": true, "where_was_i": true,
	}
	if len(res.Tools) != len(want) {
		t.Errorf("listed %d tools, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		if !want[tool.Name] {
			t.Errorf("unexpected tool %q", tool.Name)
		}
		if tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("tool %q missing description or schema", tool.Name)
		}
	}
}

func TestUnknownMethodIsAnError(t *testing.T) {
	s, _ := newTestServer(time.Now())
	req := []byte(`{"jsonrpc":"2.0","id":7,"method":"does/notExist"}` + "\n")

	var out bytes.Buffer
	if err := s.Serve(context.Background(), bytes.NewReader(req), &out); err != nil {
		t.Fatal(err)
	}
	var resp response
	json.Unmarshal(out.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("expected method-not-found error, got %+v", resp)
	}
}

func TestFleetStatusSummarizesEveryMachine(t *testing.T) {
	s, src := newTestServer(time.Now())
	text := callTool(t, s, "fleet_status", nil)

	if src.pullSeen == 0 {
		t.Error("fleet_status did not refresh from the hub")
	}

	var got struct {
		Machines []struct {
			Machine         string `json:"machine"`
			Dirty           int    `json:"dirty"`
			Unpushed        int    `json:"unpushed"`
			SnapshotsSynced int    `json:"snapshotsSynced"`
			Stale           bool   `json:"stale"`
		} `json:"machines"`
		Totals struct {
			Machines, Repos, Dirty, Unpushed int
		} `json:"totals"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if got.Totals.Machines != 2 || got.Totals.Repos != 3 {
		t.Errorf("totals = %+v", got.Totals)
	}
	if got.Totals.Dirty != 2 {
		t.Errorf("total dirty = %d, want 2", got.Totals.Dirty)
	}

	byName := map[string]int{}
	for i, m := range got.Machines {
		byName[m.Machine] = i
	}
	devbox := got.Machines[byName["devbox"]]
	// Both count: api is ahead of origin, and poppy's feature/auth branch has
	// no upstream at all, so its work exists only on devbox.
	if devbox.Unpushed != 2 {
		t.Errorf("devbox unpushed = %d, want 2 (api ahead, poppy's feature/auth has no upstream)", devbox.Unpushed)
	}
	if devbox.SnapshotsSynced != 1 {
		t.Errorf("devbox synced snapshots = %d, want 1", devbox.SnapshotsSynced)
	}
	laptop := got.Machines[byName["laptop"]]
	if !laptop.Stale {
		t.Error("laptop should be stale (last seen 90m ago, threshold 60m)")
	}
}

func TestDirtyTreesSortedOldestFirst(t *testing.T) {
	s, _ := newTestServer(time.Now())
	text := callTool(t, s, "dirty_trees", nil)

	var got struct {
		Count int `json:"count"`
		Dirty []struct {
			Machine  string  `json:"machine"`
			Repo     string  `json:"repo"`
			AgeHours float64 `json:"ageHours"`
			BackedUp bool    `json:"backedUp"`
		} `json:"dirty"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if got.Count != 2 {
		t.Fatalf("dirty count = %d, want 2", got.Count)
	}
	// poppy on devbox is dirty ~50h, notes on laptop ~2h; oldest first.
	if got.Dirty[0].Repo != "poppy" {
		t.Errorf("first dirty = %s, want poppy (oldest)", got.Dirty[0].Repo)
	}
	if !got.Dirty[0].BackedUp {
		t.Error("poppy has a synced snapshot and should read as backed up")
	}
	if got.Dirty[1].BackedUp {
		t.Error("notes has no snapshot and should not read as backed up")
	}
}

func TestFindWorkLocatesBranchAcrossMachines(t *testing.T) {
	s, _ := newTestServer(time.Now())
	text := callTool(t, s, "find_work", map[string]any{"query": "auth"})

	var got struct {
		Matches []struct {
			Machine   string   `json:"machine"`
			Repo      string   `json:"repo"`
			MatchedOn string   `json:"matchedOn"`
			Branches  []string `json:"branches"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(got.Matches))
	}
	m := got.Matches[0]
	if m.Machine != "devbox" || m.MatchedOn != "branch" {
		t.Errorf("match = %+v, want the feature/auth branch on devbox", m)
	}
}

func TestFindWorkRequiresQuery(t *testing.T) {
	s, _ := newTestServer(time.Now())
	raw := exchange(t, s, "tools/call", map[string]any{"name": "find_work", "arguments": map[string]any{}})
	var res callToolResult
	json.Unmarshal(raw, &res)
	if !res.IsError {
		t.Error("find_work with no query should report a tool error")
	}
}

func TestRepoStateReturnsFullDocument(t *testing.T) {
	s, _ := newTestServer(time.Now())
	text := callTool(t, s, "repo_state", map[string]any{"machine": "devbox", "repo": "poppy"})

	var r model.Repo
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if r.Name != "poppy" || r.Head.Branch != "feature/auth" {
		t.Errorf("repo = %+v", r)
	}
	if r.Snapshot == nil || !r.Snapshot.Synced {
		t.Error("snapshot detail missing from repo_state")
	}
}

func TestRepoStateUnknownRepoErrors(t *testing.T) {
	s, _ := newTestServer(time.Now())
	raw := exchange(t, s, "tools/call", map[string]any{
		"name": "repo_state", "arguments": map[string]any{"machine": "devbox", "repo": "ghost"},
	})
	var res callToolResult
	json.Unmarshal(raw, &res)
	if !res.IsError {
		t.Error("repo_state for a missing repo should report a tool error")
	}
}

func TestWhereWasIProducesNarrative(t *testing.T) {
	s, _ := newTestServer(time.Now())
	text := callTool(t, s, "where_was_i", map[string]any{"machine": "devbox"})

	if !strings.Contains(text, "devbox") {
		t.Errorf("digest missing machine name: %q", text)
	}
	if !strings.Contains(text, "poppy on feature/auth") {
		t.Errorf("digest missing the active branch: %q", text)
	}
	if !strings.Contains(text, "uncommitted") {
		t.Errorf("digest missing dirty summary: %q", text)
	}
}

func TestSnapshotHistoryForRemoteMachineReportsLatest(t *testing.T) {
	s, _ := newTestServer(time.Now())
	// devbox is not this machine (laptop), so only the recorded latest shows.
	text := callTool(t, s, "snapshot_history", map[string]any{"machine": "devbox", "repo": "poppy"})

	var got struct {
		Local     bool   `json:"local"`
		Note      string `json:"note"`
		Snapshots []struct {
			SHA string `json:"sha"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if got.Local {
		t.Error("devbox repo should not be reported as local to the laptop server")
	}
	if len(got.Snapshots) != 1 || got.Snapshots[0].SHA != "snap111" {
		t.Errorf("snapshots = %+v, want the recorded latest", got.Snapshots)
	}
	if got.Note == "" {
		t.Error("expected a note explaining full history is elsewhere")
	}
}

func TestMultipleMessagesInSequence(t *testing.T) {
	s, _ := newTestServer(time.Now())
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fleet_status","arguments":{}}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}

	// Three requests, one notification → exactly three response lines.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d response lines, want 3:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
		if resp.JSONRPC != "2.0" {
			t.Errorf("line %d missing jsonrpc version", i)
		}
	}
}
