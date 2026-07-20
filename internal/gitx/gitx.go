// Package gitx runs git plumbing commands. Poppy shells out to git rather
// than reimplementing it: stash-create semantics and status resolution are
// too subtle to reproduce faithfully.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds any single git invocation.
const DefaultTimeout = 30 * time.Second

// Runner executes git commands in a working directory.
type Runner struct {
	Dir     string
	Timeout time.Duration
}

// New returns a Runner rooted at dir.
func New(dir string) *Runner { return &Runner{Dir: dir, Timeout: DefaultTimeout} }

// Error carries the stderr of a failed git invocation.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// env hardens git against interfering with interactive use. Optional locks are
// disabled so a background scan never takes the index lock out from under the
// user, and prompts are suppressed so a scan can never block on credentials.
func env() []string {
	return append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C",
	)
}

// Run executes git with args and returns trimmed stdout.
func (r *Runner) Run(ctx context.Context, args ...string) (string, error) {
	out, err := r.raw(ctx, args...)
	return strings.TrimRight(string(out), "\n"), err
}

// RunZ executes git with args and splits NUL-delimited stdout, dropping the
// trailing empty field.
func (r *Runner) RunZ(ctx context.Context, args ...string) ([]string, error) {
	out, err := r.raw(ctx, args...)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(out), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	return fields, nil
}

// Lines executes git with args and returns non-empty stdout lines.
func (r *Runner) Lines(ctx context.Context, args ...string) ([]string, error) {
	out, err := r.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (r *Runner) raw(ctx context.Context, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Env = env()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// Available reports whether a git binary is usable.
func Available(ctx context.Context) error {
	_, err := New(".").Run(ctx, "version")
	return err
}
