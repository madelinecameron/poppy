package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/model"
)

// PruneAfter is how long a session may go unseen before its record is
// deleted, here and from the hub.
const PruneAfter = 30 * 24 * time.Hour

// markerName is the file a hook touches when a session changes in a way worth
// publishing straight away. The agent watches its modification time.
const markerName = ".changed"

// Dir is the local session store.
func Dir() string { return filepath.Join(config.DataDir(), "sessions") }

// FileName is the name a session's record has, here and in the hub. Session
// ids are UUIDs in practice, but the id arrives from outside, so anything
// that could leave the directory is replaced.
func FileName(sessionID string) string {
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String() + ".json"
}

// Load reads one session's record. The second return is false when there is
// none yet.
func Load(dir, sessionID string) (model.Session, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName(sessionID)))
	if errors.Is(err, os.ErrNotExist) {
		return model.Session{}, false, nil
	}
	if err != nil {
		return model.Session{}, false, err
	}
	var s model.Session
	if err := json.Unmarshal(data, &s); err != nil {
		return model.Session{}, false, fmt.Errorf("session record %s: %w", FileName(sessionID), err)
	}
	return s, true, nil
}

// List reads every session record in dir, oldest first. Records that cannot
// be read are skipped: one damaged file must not hide every other session.
func List(dir string) ([]model.Session, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []model.Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s model.Session
		if json.Unmarshal(data, &s) != nil || s.SessionID == "" {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out, nil
}

// Prune deletes records of sessions not seen within maxAge and reports how
// many went.
func Prune(dir string, now time.Time, maxAge time.Duration) (int, error) {
	all, err := List(dir)
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, s := range all {
		if now.Sub(s.LastSeen) <= maxAge {
			continue
		}
		name := filepath.Join(dir, FileName(s.SessionID))
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return pruned, err
		}
		os.Remove(strings.TrimSuffix(name, ".json") + ".lock")
		pruned++
	}
	return pruned, nil
}

// Changed reports when a hook last recorded a change worth publishing at
// once. It is the zero time when none ever has.
func Changed(dir string) time.Time {
	info, err := os.Stat(filepath.Join(dir, markerName))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func markChanged(dir string, now time.Time) error {
	path := filepath.Join(dir, markerName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return err
	}
	return os.Chtimes(path, now, now)
}

// save writes a record through a temporary file and a rename, so the agent
// reading the store never sees half of one.
func save(dir string, s model.Session) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, FileName(s.SessionID)))
}

// lock serializes hooks for one session. The harness runs them one after
// another in practice, but nothing promises it, and two read-modify-writes
// interleaving would drop a repository entry.
func lock(dir, sessionID string) (func(), error) {
	name := filepath.Join(dir, strings.TrimSuffix(FileName(sessionID), ".json")+".lock")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}
