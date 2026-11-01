package deploys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

const valid = `{"id":"abc123","time":"2026-11-01T12:00:00Z","project":"workplace","env":"prod","service":"mailbox","ok":true,"revision":"0123456789abcdef"}`

func TestParseRequiresWhatWasDeployed(t *testing.T) {
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("valid record refused: %v", err)
	}
	_, err := Parse([]byte(`{"project":"workplace"}`))
	if err == nil || !strings.Contains(err.Error(), "env, id, service, time") {
		t.Errorf("err = %v, want every missing field named", err)
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestSaveListPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deploys")
	d, _ := Parse([]byte(valid))
	if err := Save(dir, d, t0); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, d, t0); err != nil {
		t.Fatal(err)
	}
	old := d
	old.ID, old.Time = "old", t0.Add(-100*24*time.Hour)
	if err := Save(dir, old, t0); err != nil {
		t.Fatal(err)
	}

	got, err := List(dir)
	if err != nil || len(got) != 2 || got[0].ID != "old" || got[1].Revision != "0123456789abcdef" {
		t.Fatalf("list = %+v, %v; want the old one then the resaved one", got, err)
	}
	if !Changed(dir).Equal(t0) {
		t.Errorf("changed = %v, want %v", Changed(dir), t0)
	}
	if n, err := Prune(dir, t0, PruneAfter); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if got, _ := List(dir); len(got) != 1 || got[0].ID != "abc123" {
		t.Errorf("after prune = %+v", got)
	}
}

func TestFileNameCannotLeaveTheDirectory(t *testing.T) {
	if got := FileName("../../etc/passwd"); strings.Contains(got, "/") || strings.Count(got, ".") != 1 {
		t.Errorf("FileName = %q", got)
	}
}

func TestListSkipsDamagedRecords(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o644)
	os.WriteFile(filepath.Join(dir, "ok.json"), []byte(valid), 0o644)
	if got, err := List(dir); err != nil || len(got) != 1 {
		t.Errorf("list = %+v, %v", got, err)
	}
}
