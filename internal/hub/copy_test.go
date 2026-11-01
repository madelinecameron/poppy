package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madelinecameron/poppy/internal/model"
)

func publishOne(t *testing.T, s *Store, machine string, repos ...string) {
	t.Helper()
	var rs []model.Repo
	for _, name := range repos {
		rs = append(rs, repoFor(machine, name, 0))
	}
	if _, err := s.Publish(context.Background(), manifestFor(machine, len(rs)), rs); err != nil {
		t.Fatalf("publish %s: %v", machine, err)
	}
}

func TestCopyReadsTheOwnersLastCommit(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "hub")
	owner := openStore(t, dir, "")
	publishOne(t, owner, "core", "registry")

	reader, err := OpenCopy(ctx, dir)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	fleet, err := reader.Fleet()
	if err != nil {
		t.Fatalf("fleet: %v", err)
	}
	if got := machineNames(fleet); len(got) != 1 || got[0] != "core" {
		t.Fatalf("machines = %v, want [core]", got)
	}

	publishOne(t, owner, "laptop", "poppy")
	if got := machineNames(mustFleet(t, reader)); len(got) != 1 {
		t.Errorf("before Pull the reader moved to %v", got)
	}
	if err := reader.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got := machineNames(mustFleet(t, reader)); len(got) != 2 {
		t.Errorf("after Pull machines = %v, want core and laptop", got)
	}
}

// The owner's working tree changes before its commit does, and leftovers a
// reset never removes stay in it. A reader sees neither.
func TestCopyIgnoresTheWorkingTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	owner := openStore(t, dir, "")
	publishOne(t, owner, "core", "registry")
	if err := writeJSON(filepath.Join(dir, "machines", "ghost", "manifest.json"), manifestFor("ghost", 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "machines", "core")); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenCopy(context.Background(), dir)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	if got := machineNames(mustFleet(t, reader)); len(got) != 1 || got[0] != "core" {
		t.Errorf("machines = %v, want only the committed core", got)
	}
	if files, _ := reader.MachineFiles("core"); len(files) != 2 {
		t.Errorf("core files = %v, want manifest and one repo", files)
	}
}

func TestCopyWritesNothing(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "hub")
	publishOne(t, openStore(t, dir, ""), "core", "registry")
	readOnly(t, dir)

	reader, err := OpenCopy(ctx, dir)
	if err != nil {
		t.Fatalf("open copy of an unwritable hub: %v", err)
	}
	if err := reader.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := reader.Publish(ctx, manifestFor("reader", 0), nil); !errors.Is(err, errReadOnlyCopy) {
		t.Errorf("publish err = %v, want errReadOnlyCopy", err)
	}
	if _, err := reader.Retire(ctx, "core"); !errors.Is(err, errReadOnlyCopy) {
		t.Errorf("retire err = %v, want errReadOnlyCopy", err)
	}
}

func TestCopyOfSomethingThatIsNotAHub(t *testing.T) {
	if _, err := OpenCopy(context.Background(), t.TempDir()); err == nil {
		t.Fatal("OpenCopy of an empty directory succeeded")
	}
}

func mustFleet(t *testing.T, s *Store) model.Fleet {
	t.Helper()
	f, err := s.Fleet()
	if err != nil {
		t.Fatalf("fleet: %v", err)
	}
	return f
}

func TestCopyWithoutGitSaysSo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	publishOne(t, openStore(t, dir, ""), "core", "registry")
	t.Setenv("PATH", t.TempDir())

	_, err := OpenCopy(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "executable file not found") {
		t.Errorf("err = %v, want git not found", err)
	}
}

func TestCopyWithNoCommitSaysSo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	openStore(t, dir, "")

	_, err := OpenCopy(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "never published") {
		t.Errorf("err = %v, want never published", err)
	}
}
