// Package gittest builds throwaway git repositories for tests.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a repository on disk under a test's temporary directory.
type Repo struct {
	T    *testing.T
	Path string
}

// New initialises a repository with the given branch as HEAD.
func New(t *testing.T, branch string) *Repo {
	t.Helper()
	dir := t.TempDir()
	r := &Repo{T: t, Path: dir}
	r.Git("init", "-q", "-b", branch, ".")
	r.Git("config", "user.name", "test")
	r.Git("config", "user.email", "test@example.com")
	r.Git("config", "commit.gpgsign", "false")
	return r
}

// NewBare initialises a bare repository, suitable as a push target.
func NewBare(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--bare", "-b", branch, ".")
	cmd.Dir = dir
	cmd.Env = env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	return dir
}

// Git runs a git command in the repository and fails the test on error.
func (r *Repo) Git(args ...string) string {
	r.T.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Path
	cmd.Env = env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.T.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write creates or replaces a file, creating parent directories.
func (r *Repo) Write(name, content string) {
	r.T.Helper()
	path := filepath.Join(r.Path, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.T.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.T.Fatal(err)
	}
}

// Commit writes a file, stages everything, and commits.
func (r *Repo) Commit(message, name, content string) {
	r.T.Helper()
	r.Write(name, content)
	r.Git("add", "-A")
	r.Git("commit", "-q", "-m", message)
}

func env() []string {
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"HOME="+os.TempDir(),
		"LC_ALL=C",
	)
}
