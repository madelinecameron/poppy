package sessions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/model"
)

// clock hands out times a minute apart, so first and last seen differ.
type clock struct{ t time.Time }

func (c *clock) now() time.Time {
	c.t = c.t.Add(time.Minute)
	return c.t
}

func newHook(t *testing.T, harness string) (Hook, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	return Hook{Harness: harness, Dir: t.TempDir(), Now: c.now}, c
}

func mustHandle(t *testing.T, h Hook, p Payload) {
	t.Helper()
	if err := h.Handle(context.Background(), p); err != nil {
		t.Fatalf("handle %s: %v", p.Event, err)
	}
}

func mustLoad(t *testing.T, h Hook, id string) model.Session {
	t.Helper()
	rec, ok, err := Load(h.Dir, id)
	if err != nil || !ok {
		t.Fatalf("load %s: ok=%v err=%v", id, ok, err)
	}
	return rec
}

// realPath resolves symlinks the way git reports a top-level directory, so
// macOS's /var -> /private/var does not fail a path comparison.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParsePayloadReadsAClaudeSessionStart(t *testing.T) {
	p, err := ParsePayload([]byte(`{
		"session_id": "abc-123",
		"transcript_path": "/Users/mads/.claude/projects/x/abc-123.jsonl",
		"cwd": "/Users/mads/code/poppy",
		"hook_event_name": "SessionStart",
		"source": "resume",
		"permission_mode": "default"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := Payload{
		SessionID: "abc-123", TranscriptPath: "/Users/mads/.claude/projects/x/abc-123.jsonl",
		Cwd: "/Users/mads/code/poppy", Event: "SessionStart", Source: "resume",
	}
	if p != want {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

func TestParsePayloadReadsACodexPrompt(t *testing.T) {
	p, err := ParsePayload([]byte(`{"session_id":"0199-codex","transcript_path":"/home/m/.codex/sessions/rollout.jsonl","cwd":"/home/m/code/api","hook_event_name":"UserPromptSubmit","model":"gpt-5","prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "0199-codex" || p.Event != "UserPromptSubmit" || p.Cwd != "/home/m/code/api" {
		t.Errorf("payload = %+v", p)
	}
}

func TestParsePayloadToleratesMissingFields(t *testing.T) {
	p, err := ParsePayload([]byte(`{"session_id":"only-id"}`))
	if err != nil {
		t.Fatalf("a payload with only a session id was refused: %v", err)
	}
	if p.Cwd != "" || p.Event != "" || p.TranscriptPath != "" {
		t.Errorf("absent fields were filled: %+v", p)
	}
}

func TestParsePayloadRefusesWhatCannotBeFiled(t *testing.T) {
	for name, input := range map[string]string{
		"garbage":    "not json at all",
		"empty":      "",
		"array":      `["session_id"]`,
		"no id":      `{"cwd":"/tmp","hook_event_name":"Stop"}`,
		"blank id":   `{"session_id":"   "}`,
		"wrong type": `{"session_id":42}`,
	} {
		if _, err := ParsePayload([]byte(input)); err == nil {
			t.Errorf("%s: accepted %q", name, input)
		}
	}
}

func TestHookRecordsTheSessionAndItsRepository(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	h, _ := newHook(t, Claude)

	mustHandle(t, h, Payload{
		SessionID: "s1", TranscriptPath: "/t/s1.jsonl", Cwd: filepath.Join(repo.Path),
		Event: EventSessionStart, Source: "startup",
	})

	rec := mustLoad(t, h, "s1")
	if rec.Harness != Claude || rec.SessionID != "s1" || rec.TranscriptPath != "/t/s1.jsonl" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Schema != model.SchemaVersion {
		t.Errorf("schema = %d, want %d", rec.Schema, model.SchemaVersion)
	}
	if len(rec.Repos) != 1 {
		t.Fatalf("repos = %+v, want one", rec.Repos)
	}
	r := rec.Repos[0]
	if r.Path != realPath(t, repo.Path) || r.Branch != "main" || r.Name != filepath.Base(repo.Path) {
		t.Errorf("repo entry = %+v", r)
	}
	if rec.Ended != nil {
		t.Error("a starting session is recorded as ended")
	}
}

// A subdirectory of a working tree is the same repository.
func TestHookResolvesASubdirectoryToItsWorkingTree(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "pkg/a.txt", "one\n")
	h, _ := newHook(t, Codex)

	mustHandle(t, h, Payload{SessionID: "s1", Cwd: filepath.Join(repo.Path, "pkg"), Event: "UserPromptSubmit"})

	rec := mustLoad(t, h, "s1")
	if len(rec.Repos) != 1 || rec.Repos[0].Path != realPath(t, repo.Path) {
		t.Errorf("repos = %+v, want the top level", rec.Repos)
	}
}

// The case "rev-parse --abbrev-ref HEAD" gets wrong: a branch with no commits.
func TestHookNamesAnUnbornBranch(t *testing.T) {
	repo := gittest.New(t, "feature/new")
	h, _ := newHook(t, Claude)

	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: EventSessionStart})

	rec := mustLoad(t, h, "s1")
	if len(rec.Repos) != 1 || rec.Repos[0].Branch != "feature/new" {
		t.Errorf("repos = %+v, want feature/new", rec.Repos)
	}
}

func TestHookRecordsADetachedHeadWithoutABranch(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	repo.Git("checkout", "-q", "--detach")
	h, _ := newHook(t, Claude)

	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: "UserPromptSubmit"})

	rec := mustLoad(t, h, "s1")
	if len(rec.Repos) != 1 || rec.Repos[0].Branch != "" {
		t.Errorf("repos = %+v, want one entry with no branch", rec.Repos)
	}
}

func TestHookCapturesBranchSwitches(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	h, _ := newHook(t, Claude)

	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: EventSessionStart})
	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: "Stop"})

	repo.Git("checkout", "-q", "-b", "feat/sessions")
	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: "UserPromptSubmit"})

	repo.Git("checkout", "-q", "main")
	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: "UserPromptSubmit"})

	rec := mustLoad(t, h, "s1")
	if len(rec.Repos) != 2 {
		t.Fatalf("repos = %+v, want main and feat/sessions", rec.Repos)
	}
	main, feat := rec.Repos[0], rec.Repos[1]
	if main.Branch != "main" || feat.Branch != "feat/sessions" {
		t.Fatalf("branches = %q, %q, want main then feat/sessions", main.Branch, feat.Branch)
	}
	if !main.LastSeen.After(feat.LastSeen) {
		t.Error("returning to main did not update main's last seen")
	}
	if !main.FirstSeen.Before(feat.FirstSeen) {
		t.Error("main's first seen moved on return")
	}
	if !rec.LastSeen.Equal(main.LastSeen) {
		t.Errorf("session last seen %v, want the latest event %v", rec.LastSeen, main.LastSeen)
	}
}

func TestHookRecordsASessionOutsideAnyRepository(t *testing.T) {
	h, _ := newHook(t, Claude)

	mustHandle(t, h, Payload{SessionID: "s1", Cwd: t.TempDir(), Event: EventSessionStart})
	mustHandle(t, h, Payload{SessionID: "s1", Cwd: filepath.Join(t.TempDir(), "gone"), Event: "Stop"})
	mustHandle(t, h, Payload{SessionID: "s1", Event: "Stop"})

	rec := mustLoad(t, h, "s1")
	if len(rec.Repos) != 0 {
		t.Errorf("repos = %+v, want none", rec.Repos)
	}
	if rec.LastEvent != "Stop" {
		t.Errorf("last event = %q", rec.LastEvent)
	}
}

func TestStopDoesNotEndASession(t *testing.T) {
	h, _ := newHook(t, Claude)
	mustHandle(t, h, Payload{SessionID: "s1", Event: EventSessionStart})
	mustHandle(t, h, Payload{SessionID: "s1", Event: "Stop"})

	if rec := mustLoad(t, h, "s1"); rec.Ended != nil {
		t.Error("Stop ended the session; Claude Code fires it after every turn")
	}

	mustHandle(t, h, Payload{SessionID: "s1", Event: EventSessionEnd})
	rec := mustLoad(t, h, "s1")
	if rec.Ended == nil {
		t.Fatal("SessionEnd did not end the session")
	}

	mustHandle(t, h, Payload{SessionID: "s1", Event: EventSessionStart, Source: "resume"})
	if rec := mustLoad(t, h, "s1"); rec.Ended != nil {
		t.Error("a resumed session is still recorded as ended")
	}
}

func TestMailboxIsLookedUpAtSessionStartOnly(t *testing.T) {
	h, _ := newHook(t, Claude)
	calls := 0
	h.Address = func(_ context.Context, id string) (string, error) {
		calls++
		return "laptop-key." + id, nil
	}

	mustHandle(t, h, Payload{SessionID: "s1", Event: EventSessionStart})
	for _, ev := range []string{"UserPromptSubmit", "Stop", "UserPromptSubmit", "Stop"} {
		mustHandle(t, h, Payload{SessionID: "s1", Event: ev})
	}

	if calls != 1 {
		t.Errorf("whoami called %d times, want once", calls)
	}
	if rec := mustLoad(t, h, "s1"); rec.Mailbox != "laptop-key.s1" {
		t.Errorf("mailbox = %q", rec.Mailbox)
	}
}

// A mailbox that does not answer costs the address, not the record, and an
// address already known survives a failed lookup on resume.
func TestAFailedLookupStillRecordsTheEvent(t *testing.T) {
	h, _ := newHook(t, Claude)
	h.Address = func(context.Context, string) (string, error) { return "addr.s1", nil }
	mustHandle(t, h, Payload{SessionID: "s1", Event: EventSessionStart})

	h.Address = func(context.Context, string) (string, error) { return "", errors.New("connection refused") }
	err := h.Handle(context.Background(), Payload{SessionID: "s1", Event: EventSessionStart, Source: "resume"})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v, want the lookup failure reported", err)
	}
	rec := mustLoad(t, h, "s1")
	if rec.Mailbox != "addr.s1" {
		t.Errorf("mailbox = %q, want the earlier address kept", rec.Mailbox)
	}
	if rec.LastEvent != EventSessionStart {
		t.Errorf("last event = %q", rec.LastEvent)
	}
}

func TestHookRefusesAnUnknownHarness(t *testing.T) {
	h, _ := newHook(t, "cursor")
	if err := h.Handle(context.Background(), Payload{SessionID: "s1"}); err == nil {
		t.Fatal("an unknown harness was accepted")
	}
	if _, ok, _ := Load(h.Dir, "s1"); ok {
		t.Error("an unknown harness wrote a record")
	}
}

// A damaged record is replaced so the rest of the session is still tracked.
func TestHookReplacesADamagedRecord(t *testing.T) {
	h, _ := newHook(t, Claude)
	if err := os.WriteFile(filepath.Join(h.Dir, FileName("s1")), []byte("{half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.Background(), Payload{SessionID: "s1", Event: "Stop"}); err == nil {
		t.Error("the damaged record was not reported")
	}
	if rec := mustLoad(t, h, "s1"); rec.LastEvent != "Stop" {
		t.Errorf("record = %+v", rec)
	}
}

// The agent publishes straight away only on changes another agent cares
// about; a later last-seen waits for the scan.
func TestTheMarkerMovesOnlyOnMaterialChanges(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")
	h, _ := newHook(t, Claude)

	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: EventSessionStart})
	first := Changed(h.Dir)
	if first.IsZero() {
		t.Fatal("a new session did not mark a change")
	}

	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: "Stop"})
	if !Changed(h.Dir).Equal(first) {
		t.Error("a Stop in the same place marked a change")
	}

	repo.Git("checkout", "-q", "-b", "other")
	mustHandle(t, h, Payload{SessionID: "s1", Cwd: repo.Path, Event: "UserPromptSubmit"})
	if !Changed(h.Dir).After(first) {
		t.Error("a branch switch did not mark a change")
	}
}

func TestFileNameCannotLeaveTheDirectory(t *testing.T) {
	for _, id := range []string{"../../etc/passwd", "a/b", `a\b`, "..", "x.json"} {
		name := FileName(id)
		if strings.ContainsAny(strings.TrimSuffix(name, ".json"), `/\.`) {
			t.Errorf("FileName(%q) = %q", id, name)
		}
	}
	if got := FileName("0b6c3f2e-1d2a-4e5f-9a8b-7c6d5e4f3a2b"); got != "0b6c3f2e-1d2a-4e5f-9a8b-7c6d5e4f3a2b.json" {
		t.Errorf("a UUID was altered: %q", got)
	}
}

func TestPruneDeletesSessionsNotSeenForThirtyDays(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for id, seen := range map[string]time.Time{
		"fresh": now.Add(-time.Hour),
		"edge":  now.Add(-PruneAfter + time.Minute),
		"old":   now.Add(-PruneAfter - time.Hour),
	} {
		if err := save(dir, model.Session{SessionID: id, FirstSeen: seen, LastSeen: seen}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := Prune(dir, now, PruneAfter)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d (err %v), want 1", n, err)
	}
	left, _ := List(dir)
	if len(left) != 2 {
		t.Errorf("left %+v, want fresh and edge", left)
	}
	for _, s := range left {
		if s.SessionID == "old" {
			t.Error("the old session survived")
		}
	}
}

func TestParseTokenReadsTheFormsPeopleWrite(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"single quotes": {"export MAILBOX_TOKEN='mbx_abc123'\n", "mbx_abc123"},
		"double quotes": {`export MAILBOX_TOKEN="mbx_dq"` + "\n", "mbx_dq"},
		"bare":          {"export MAILBOX_TOKEN=mbx_bare # laptop\n", "mbx_bare"},
		"no export":     {"MAILBOX_TOKEN='mbx_plain'\n", "mbx_plain"},
		"indented":      {"  export MAILBOX_TOKEN='mbx_ind'\n", "mbx_ind"},
		"among others": {
			"export GITHUB_TOKEN='ghp_x'\n# export MAILBOX_TOKEN='mbx_commented'\nexport MAILBOX_TOKEN='mbx_real'\nexport OTHER=1\n",
			"mbx_real",
		},
		"last wins":       {"export MAILBOX_TOKEN='mbx_one'\nexport MAILBOX_TOKEN='mbx_two'\n", "mbx_two"},
		"absent":          {"export GITHUB_TOKEN='ghp_x'\n", ""},
		"similar name":    {"export MAILBOX_TOKEN_OLD='mbx_no'\n", ""},
		"unclosed quote":  {"export MAILBOX_TOKEN='mbx_broken\n", ""},
		"empty file":      {"", ""},
		"empty assigment": {"export MAILBOX_TOKEN=''\n", ""},
	} {
		if got := parseToken([]byte(tc.src)); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestTokenPrefersTheEnvironmentThenTheSecretsFile(t *testing.T) {
	zdot := t.TempDir()
	if err := os.WriteFile(filepath.Join(zdot, "secrets.zsh"), []byte("export MAILBOX_TOKEN='mbx_file'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ZDOTDIR": zdot, "HOME": t.TempDir()}
	getenv := func(k string) string { return env[k] }

	if got := Token(getenv); got != "mbx_file" {
		t.Errorf("from the file: %q", got)
	}
	env["MAILBOX_TOKEN"] = "mbx_env"
	if got := Token(getenv); got != "mbx_env" {
		t.Errorf("with the environment set: %q", got)
	}
}

func TestSecretsPathFallsBackToHomeZsh(t *testing.T) {
	got := SecretsPath(func(k string) string { return map[string]string{"HOME": "/home/m"}[k] })
	if got != "/home/m/.zsh/secrets.zsh" {
		t.Errorf("path = %q", got)
	}
	got = SecretsPath(func(k string) string { return map[string]string{"HOME": "/home/m", "ZDOTDIR": "/z"}[k] })
	if got != "/z/secrets.zsh" {
		t.Errorf("with ZDOTDIR: %q", got)
	}
}

func TestWhoamiSendsTheTokenAndSession(t *testing.T) {
	var gotPath, gotAuth, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotSession = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Mailbox-Session")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"address":"k7f.sess-1","machine":"laptop"}`))
	}))
	defer srv.Close()

	addr, err := Whoami(context.Background(), srv.Client(), srv.URL+"/", "mbx_secret", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if addr != "k7f.sess-1" {
		t.Errorf("address = %q", addr)
	}
	if gotPath != "/v1/whoami" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer mbx_secret" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if gotSession != "sess-1" {
		t.Errorf("session header = %q", gotSession)
	}
}

func TestWhoamiGivesUpAfterTwoSeconds(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, err := Whoami(context.Background(), srv.Client(), srv.URL, "t", "s")
	took := time.Since(start)
	if err == nil {
		t.Fatal("a server that never answered produced an address")
	}
	if took < WhoamiTimeout-100*time.Millisecond || took > WhoamiTimeout+time.Second {
		t.Errorf("gave up after %s, want about %s", took, WhoamiTimeout)
	}
}

func TestWhoamiReportsARefusalWithoutTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := Whoami(context.Background(), srv.Client(), srv.URL, "mbx_secret", "s")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want the status", err)
	}
	if strings.Contains(err.Error(), "mbx_secret") {
		t.Error("the error carries the token")
	}
}

func TestWhoamiRefusesAnAnswerWithNoAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"machine":"laptop"}`))
	}))
	defer srv.Close()

	if _, err := Whoami(context.Background(), srv.Client(), srv.URL, "t", "s"); err == nil {
		t.Error("an answer without an address was accepted")
	}
}
