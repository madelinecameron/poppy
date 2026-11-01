package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/hub"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/sessions"
)

// runHook runs "poppy <args>" through Main with stdin fed from input, and
// returns the exit code and everything written to stdout and stderr.
func runHook(t *testing.T, input string, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "stdin")
	if err := os.WriteFile(in, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	out, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdin, out, out
	code := Main(context.Background(), args)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr

	written, _ := os.ReadFile(out.Name())
	return code, string(written)
}

func isolateHook(t *testing.T) string {
	t.Helper()
	dir := isolate(t)
	t.Setenv("MAILBOX_URL", "")
	t.Setenv("MAILBOX_TOKEN", "")
	return dir
}

// Claude Code injects a hook's stdout into the model's context and treats a
// failing hook as the user's problem, so every path through the hook must be
// silent and exit zero, including the ones that go wrong.
func TestSessionHookIsSilentAndExitsZero(t *testing.T) {
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	for name, tc := range map[string]struct {
		input string
		args  []string
	}{
		"claude start":    {`{"session_id":"s1","cwd":"` + repo.Path + `","hook_event_name":"SessionStart","source":"startup"}`, []string{"session-hook", "claude"}},
		"codex prompt":    {`{"session_id":"s2","cwd":"` + repo.Path + `","hook_event_name":"UserPromptSubmit"}`, []string{"session-hook", "codex"}},
		"garbage":         {"this is not json", []string{"session-hook", "claude"}},
		"empty stdin":     {"", []string{"session-hook", "claude"}},
		"no session id":   {`{"cwd":"/tmp"}`, []string{"session-hook", "claude"}},
		"unknown harness": {`{"session_id":"s3"}`, []string{"session-hook", "cursor"}},
		"no harness":      {`{"session_id":"s4"}`, []string{"session-hook"}},
		"extra args":      {`{"session_id":"s5"}`, []string{"session-hook", "claude", "--verbose"}},
	} {
		isolateHook(t)
		code, out := runHook(t, tc.input, tc.args...)
		if code != 0 {
			t.Errorf("%s: exit %d, want 0", name, code)
		}
		if out != "" {
			t.Errorf("%s: wrote %q, want nothing", name, out)
		}
	}
}

func TestSessionHookRecordsLocallyAndLogsFailures(t *testing.T) {
	dir := isolateHook(t)
	repo := gittest.New(t, "main")
	repo.Commit("init", "a.txt", "one\n")

	runHook(t, `{"session_id":"s1","transcript_path":"/t/s1.jsonl","cwd":"`+repo.Path+`","hook_event_name":"SessionStart"}`, "session-hook", "claude")
	rec, ok, err := sessions.Load(filepath.Join(dir, "sessions"), "s1")
	if err != nil || !ok {
		t.Fatalf("no local record: ok=%v err=%v", ok, err)
	}
	if rec.Harness != "claude" || len(rec.Repos) != 1 || rec.Repos[0].Branch != "main" {
		t.Errorf("record = %+v", rec)
	}

	runHook(t, "{not json", "session-hook", "claude")
	log, err := os.ReadFile(filepath.Join(dir, "session-hook.log"))
	if err != nil {
		t.Fatalf("no hook log: %v", err)
	}
	if !strings.Contains(string(log), "not a JSON object") {
		t.Errorf("log does not say what went wrong:\n%s", log)
	}
}

// The token reaches the mailbox and nowhere else: not the record, not the log.
func TestSessionHookLooksUpTheMailboxAndKeepsTheTokenOut(t *testing.T) {
	dir := isolateHook(t)
	var gotAuth, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotSession = r.Header.Get("Authorization"), r.Header.Get("X-Mailbox-Session")
		if r.Header.Get("X-Mailbox-Session") == "bad" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		io.WriteString(w, `{"address":"k9.`+gotSession+`"}`)
	}))
	defer srv.Close()
	t.Setenv("MAILBOX_URL", srv.URL)
	t.Setenv("ZDOTDIR", t.TempDir())
	t.Setenv("MAILBOX_TOKEN", "mbx_supersecret")

	runHook(t, `{"session_id":"s1","hook_event_name":"SessionStart"}`, "session-hook", "claude")
	if gotAuth != "Bearer mbx_supersecret" || gotSession != "s1" {
		t.Errorf("whoami saw auth=%q session=%q", gotAuth, gotSession)
	}
	rec, _, _ := sessions.Load(filepath.Join(dir, "sessions"), "s1")
	if rec.Mailbox != "k9.s1" {
		t.Errorf("mailbox = %q", rec.Mailbox)
	}

	runHook(t, `{"session_id":"bad","hook_event_name":"SessionStart"}`, "session-hook", "claude")
	for _, name := range []string{"sessions/s1.json", "sessions/bad.json", "session-hook.log"} {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		if strings.Contains(string(data), "mbx_supersecret") {
			t.Errorf("%s contains the token", name)
		}
	}
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdout
	os.Stdout = f
	err = fn()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Name())
	return string(data)
}

func TestSessionsCommandFiltersAndShowsHowToReachEach(t *testing.T) {
	dir := isolate(t)
	ctx := context.Background()
	store, err := hub.Open(ctx, filepath.Join(dir, "hub"), "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ended := now.Add(-time.Hour)
	store.Publish(ctx, model.Manifest{Schema: model.SchemaVersion, Machine: "laptop", Heartbeat: now}, nil)
	store.PublishSessions(ctx, "laptop", []model.Session{
		{
			Harness: "claude", SessionID: "live-1", Mailbox: "k1.live-1", TranscriptPath: "/t/live-1.jsonl",
			FirstSeen: now.Add(-time.Hour), LastSeen: now.Add(-2 * time.Minute),
			Repos: []model.SessionRepo{{Path: "/code/poppy", Name: "poppy", RepoID: "id-poppy", Branch: "feat/sessions", FirstSeen: now.Add(-time.Hour), LastSeen: now}},
		},
		{
			Harness: "codex", SessionID: "done-2", FirstSeen: now.Add(-3 * time.Hour), LastSeen: now.Add(-time.Hour), Ended: &ended,
			Repos: []model.SessionRepo{{Path: "/code/api", Name: "api", RepoID: "id-api", Branch: "main"}},
		},
	})

	all := captureStdout(t, func() error { return runSessions(ctx, []string{"--no-pull"}) })
	for _, want := range []string{"2 sessions", "1 active", "laptop:claude", "live-1", "k1.live-1", "/t/live-1.jsonl", "feat/sessions", "/code/poppy", "laptop:codex", "ended", "(none)"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}

	for name, tc := range map[string]struct {
		args      []string
		want, not string
	}{
		"repo":   {[]string{"--repo", "api"}, "done-2", "live-1"},
		"repoID": {[]string{"--repo", "id-poppy"}, "live-1", "done-2"},
		"branch": {[]string{"--branch", "feat/sessions"}, "live-1", "done-2"},
		"active": {[]string{"--active"}, "live-1", "done-2"},
	} {
		out := captureStdout(t, func() error { return runSessions(ctx, append([]string{"--no-pull"}, tc.args...)) })
		if !strings.Contains(out, tc.want) || strings.Contains(out, tc.not) {
			t.Errorf("%s: want %s and not %s:\n%s", name, tc.want, tc.not, out)
		}
	}

	none := captureStdout(t, func() error { return runSessions(ctx, []string{"--no-pull", "--machine", "devbox"}) })
	if !strings.Contains(none, "No sessions match") {
		t.Errorf("an empty result does not say so:\n%s", none)
	}
}
