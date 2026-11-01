// Package sessions records Claude Code and Codex harness sessions on this
// machine: which repositories and branches each worked in, and the mailbox
// address that reaches it.
//
// The harness runs "poppy session-hook <harness>" on its lifecycle events with
// a JSON payload on stdin. The hook updates one small file per session in
// poppy's local state directory, and the agent publishes those files to the
// hub. The hook never touches the hub itself: it runs on every prompt, and a
// git fetch and push there would put network latency in front of the user.
package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/gitx"
	"github.com/madelinecameron/poppy/internal/model"
)

// Harnesses poppy accepts session hooks from.
const (
	Claude = "claude"
	Codex  = "codex"
)

// Hook event names. Stop is deliberately not an end: Claude Code fires it at
// the end of every turn.
const (
	EventSessionStart = "SessionStart"
	EventSessionEnd   = "SessionEnd"
)

// MaxRepos bounds how many repository and branch entries one session keeps.
// The least recently seen entry is dropped first.
const MaxRepos = 50

// gitTimeout bounds the one git call a hook makes. A hook that hangs holds up
// the user's prompt.
const gitTimeout = 2 * time.Second

// ValidHarness reports whether poppy knows the named harness.
func ValidHarness(h string) bool { return h == Claude || h == Codex }

// Payload is the part of a hook's stdin poppy reads. Claude Code and Codex
// send the same field names; anything else in the object is ignored, and any
// of these may be absent.
type Payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Event          string `json:"hook_event_name"`
	// Source says why a session started: startup, resume, or clear.
	Source string `json:"source"`
}

// ParsePayload decodes a hook payload. Only a session id is required, since
// without one there is nothing to file the event under.
func ParsePayload(data []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return Payload{}, fmt.Errorf("hook payload is not a JSON object: %w", err)
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return Payload{}, errors.New("hook payload has no session_id")
	}
	return p, nil
}

// Hook applies hook events to the local session store.
type Hook struct {
	Harness string
	// Dir is the local session store, normally Dir().
	Dir string
	Now func() time.Time
	// Address looks up the session's mailbox address. It is called at
	// SessionStart only, and nil skips the lookup.
	Address func(ctx context.Context, sessionID string) (string, error)
}

// Handle records one event. The record is written even when part of the work
// fails, such as the mailbox lookup, and the failure is returned so the
// caller can log it.
func (h Hook) Handle(ctx context.Context, p Payload) error {
	if !ValidHarness(h.Harness) {
		return fmt.Errorf("unknown harness %q; want %s or %s", h.Harness, Claude, Codex)
	}
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := lock(h.Dir, p.SessionID)
	if err != nil {
		return err
	}
	defer unlock()

	var problems []error
	rec, found, err := Load(h.Dir, p.SessionID)
	if err != nil {
		// A record that cannot be read is replaced rather than left to fail
		// every later event of the session.
		problems = append(problems, err)
	}

	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	before := rec
	before.Repos = append([]model.SessionRepo(nil), rec.Repos...)

	if !found {
		rec = model.Session{SessionID: p.SessionID, FirstSeen: now}
	}
	rec.Schema = model.SchemaVersion
	rec.Harness = h.Harness
	rec.LastSeen = now
	if p.Event != "" {
		rec.LastEvent = p.Event
	}
	if p.TranscriptPath != "" {
		rec.TranscriptPath = p.TranscriptPath
	}

	switch p.Event {
	case EventSessionStart:
		// A resumed session is live again whatever ended it before.
		rec.Ended = nil
		if h.Address != nil {
			addr, err := h.Address(ctx, p.SessionID)
			switch {
			case err != nil:
				problems = append(problems, fmt.Errorf("mailbox whoami: %w", err))
			case addr != "":
				rec.Mailbox = addr
			}
		}
	case EventSessionEnd:
		rec.Ended = &now
	}

	if p.Cwd != "" {
		top, branch, err := resolveRepo(ctx, p.Cwd)
		switch {
		case err != nil:
			problems = append(problems, err)
		case top != "":
			touchRepo(&rec, top, branch, now)
		}
	}

	if err := save(h.Dir, rec); err != nil {
		return errors.Join(append(problems, err)...)
	}
	if !found || material(before, rec) {
		if err := markChanged(h.Dir, now); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

// touchRepo records that the session is in the repository at top on branch.
// Returning to a repository and branch it was in before updates that entry
// rather than adding a second one.
func touchRepo(rec *model.Session, top, branch string, now time.Time) {
	for i := range rec.Repos {
		if rec.Repos[i].Path == top && rec.Repos[i].Branch == branch {
			rec.Repos[i].LastSeen = now
			return
		}
	}
	rec.Repos = append(rec.Repos, model.SessionRepo{
		Path: top, Name: filepath.Base(top), Branch: branch,
		FirstSeen: now, LastSeen: now,
	})
	if len(rec.Repos) > MaxRepos {
		oldest := 0
		for i, r := range rec.Repos {
			if r.LastSeen.Before(rec.Repos[oldest].LastSeen) {
				oldest = i
			}
		}
		rec.Repos = append(rec.Repos[:oldest], rec.Repos[oldest+1:]...)
	}
}

// material reports whether a record changed in a way another agent would want
// to hear about before the next scheduled publish: a new repository or
// branch, a mailbox address, an end, or a new transcript. A later LastSeen
// alone is not one; it rides the regular scan.
func material(before, after model.Session) bool {
	if before.Mailbox != after.Mailbox ||
		before.TranscriptPath != after.TranscriptPath ||
		(before.Ended == nil) != (after.Ended == nil) ||
		len(before.Repos) != len(after.Repos) {
		return true
	}
	for i := range before.Repos {
		if before.Repos[i].Path != after.Repos[i].Path || before.Repos[i].Branch != after.Repos[i].Branch {
			return true
		}
	}
	return false
}

// resolveRepo finds the working tree containing dir and its checked-out
// branch with a single git call. The branch is read from HEAD directly
// because "rev-parse --abbrev-ref HEAD" fails on a branch with no commits
// yet, which is exactly where a new session often starts.
//
// A directory outside any repository returns an empty top and no error.
func resolveRepo(ctx context.Context, dir string) (top, branch string, err error) {
	g := gitx.New(dir)
	g.Timeout = gitTimeout
	lines, err := g.Lines(ctx, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	if err != nil {
		var gerr *gitx.Error
		if errors.As(err, &gerr) && strings.Contains(gerr.Stderr, "not a git repository") {
			return "", "", nil
		}
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", "", nil
		}
		return "", "", err
	}
	if len(lines) < 2 || lines[0] == "" {
		// A bare repository or a .git directory has no working tree.
		return "", "", nil
	}
	top, gitDir := lines[0], lines[1]

	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return top, "", nil
	}
	ref, symbolic := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: ")
	if !symbolic {
		return top, "", nil
	}
	branch = strings.TrimPrefix(ref, "refs/heads/")
	if branch == ".invalid" {
		// A reftable repository keeps a placeholder in HEAD; ask git.
		branch, _ = g.Run(ctx, "branch", "--show-current")
	}
	return top, branch, nil
}
