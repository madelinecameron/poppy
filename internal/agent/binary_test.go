package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplacedByRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "poppy")
	write(t, path, "v1")
	started := stat(t, path)
	if replaced(path, started) {
		t.Fatal("an untouched binary reads as replaced")
	}

	next := filepath.Join(dir, "poppy.new")
	write(t, next, "v2")
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if !replaced(path, started) {
		t.Error("a binary renamed over the original is not detected")
	}
}

func TestReplacedInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poppy")
	write(t, path, "v1")
	started := stat(t, path)

	write(t, path, "v2-longer")
	if !replaced(path, started) {
		t.Error("a binary rewritten in place is not detected")
	}
}

func TestReplacedWhileMissingWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poppy")
	write(t, path, "v1")
	started := stat(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if replaced(path, started) {
		t.Error("a binary missing mid-swap triggered a restart")
	}
}

func TestRunReturnsWhenBinaryReplaced(t *testing.T) {
	a := agentFor(t, "laptop", t.TempDir(), filepath.Join(t.TempDir(), "hub"), "")
	a.Replaced = func() bool { return true }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Run(ctx); !errors.Is(err, ErrBinaryReplaced) {
		t.Fatalf("Run = %v, want ErrBinaryReplaced", err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
