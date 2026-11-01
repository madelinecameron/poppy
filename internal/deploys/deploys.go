// Package deploys keeps the deploys houseparty ran on this machine until the
// agent publishes them to the hub. houseparty hands each one over with
// `poppy deploy-record`; poppy never runs or inspects a deploy itself.
package deploys

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

// PruneAfter is how long a deploy is kept, here and in the hub.
const PruneAfter = 90 * 24 * time.Hour

// markerName is touched when a deploy arrives, so the agent publishes it
// without waiting for the next scan.
const markerName = ".changed"

// Dir is the local deploy store.
func Dir() string { return filepath.Join(config.DataDir(), "deploys") }

// FileName is the name a deploy's record has, here and in the hub. The id
// arrives from outside, so anything that could leave the directory is
// replaced.
func FileName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String() + ".json"
}

// Parse reads one deploy record and checks it names what was deployed.
func Parse(data []byte) (model.Deploy, error) {
	var d model.Deploy
	if err := json.Unmarshal(data, &d); err != nil {
		return d, fmt.Errorf("deploy record: %w", err)
	}
	var missing []string
	for name, v := range map[string]string{"id": d.ID, "project": d.Project, "env": d.Env, "service": d.Service} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if d.Time.IsZero() {
		missing = append(missing, "time")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return d, fmt.Errorf("deploy record has no %s", strings.Join(missing, ", "))
	}
	return d, nil
}

// Save writes a deploy into dir and marks the store changed. Saving the same
// id again replaces it.
func Save(dir string, d model.Deploy, now time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".deploy-*")
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
	if err := os.Rename(tmp.Name(), filepath.Join(dir, FileName(d.ID))); err != nil {
		return err
	}
	marker := filepath.Join(dir, markerName)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return err
	}
	return os.Chtimes(marker, now, now)
}

// List reads every deploy in dir, oldest first. A record that cannot be read
// is skipped so it does not hide the rest.
func List(dir string) ([]model.Deploy, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []model.Deploy
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if d, err := Parse(data); err == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// Prune deletes deploys older than maxAge and reports how many went.
func Prune(dir string, now time.Time, maxAge time.Duration) (int, error) {
	all, err := List(dir)
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, d := range all {
		if now.Sub(d.Time) <= maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, FileName(d.ID))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}

// Changed reports when a deploy last arrived. It is the zero time when none
// ever has.
func Changed(dir string) time.Time {
	info, err := os.Stat(filepath.Join(dir, markerName))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
