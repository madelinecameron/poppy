// Package hooks installs and verifies poppy's pre-commit secret hook.
//
// The hook is the cheap half of the problem: it stops the next credential from
// being committed and does nothing about the ones already there. Its value
// here is that poppy knows about every repository on every machine, so
// "which of my repositories are unprotected" becomes a question with an
// answer.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/madelinecameron/poppy/internal/gitx"
)

// Version is the generation of the installed script. Raising it makes existing
// poppy-managed hooks report as outdated so they can be refreshed.
const Version = 1

// marker identifies a hook poppy wrote. Anything without it belongs to a
// person and is never touched without an explicit instruction.
const marker = "poppy-managed pre-commit hook"

// State is what verify found in a repository.
type State string

const (
	// StateInstalled is poppy's hook, current.
	StateInstalled State = "installed"
	// StateOutdated is poppy's hook from an earlier version.
	StateOutdated State = "outdated"
	// StateMissing is no pre-commit hook at all.
	StateMissing State = "missing"
	// StateForeign is a pre-commit hook somebody else wrote. Installing over
	// one requires an explicit flag.
	StateForeign State = "foreign"
	// StateError is a repository whose hook path could not be read.
	StateError State = "error"
)

// Status is one repository's hook state.
type Status struct {
	Repo  string
	Hook  string
	State State
	Err   error
}

// Protected reports whether a commit in this repository would be scanned.
func (s Status) Protected() bool { return s.State == StateInstalled || s.State == StateOutdated }

// Verify reports the pre-commit hook state of one repository.
func Verify(ctx context.Context, repo string) Status {
	path, err := hookPath(ctx, repo)
	if err != nil {
		return Status{Repo: repo, State: StateError, Err: err}
	}
	st := Status{Repo: repo, Hook: path}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		st.State = StateMissing
		return st
	case err != nil:
		st.State, st.Err = StateError, err
		return st
	}

	body := string(data)
	switch {
	case !strings.Contains(body, marker):
		st.State = StateForeign
	case strings.Contains(body, versionLine()):
		st.State = StateInstalled
	default:
		st.State = StateOutdated
	}
	return st
}

// Install writes the hook into one repository.
//
// A hook somebody wrote is never replaced silently: without force the
// repository is reported as a conflict and left exactly as it was. With force
// the existing script is moved aside rather than deleted, because a hook is
// somebody's work too.
func Install(ctx context.Context, repo string, force bool) (Status, error) {
	st := Verify(ctx, repo)
	switch {
	case st.State == StateError:
		return st, st.Err
	case st.State == StateForeign && !force:
		return st, nil
	case st.State == StateInstalled && !force:
		return st, nil
	}

	if st.State == StateForeign {
		backup, err := backupPath(st.Hook)
		if err != nil {
			return st, err
		}
		if err := os.Rename(st.Hook, backup); err != nil {
			return st, err
		}
	}

	if err := os.MkdirAll(filepath.Dir(st.Hook), 0o755); err != nil {
		return st, err
	}
	if err := os.WriteFile(st.Hook, []byte(script()), 0o755); err != nil {
		return st, err
	}
	st.State = StateInstalled
	return st, nil
}

// Uninstall removes a poppy-managed hook and restores whatever it displaced.
// A hook poppy did not write is left alone.
func Uninstall(ctx context.Context, repo string) (Status, error) {
	st := Verify(ctx, repo)
	if !st.Protected() {
		return st, nil
	}
	if err := os.Remove(st.Hook); err != nil {
		return st, err
	}
	st.State = StateMissing

	backup := st.Hook + ".poppy-backup"
	if _, err := os.Stat(backup); err == nil {
		if err := os.Rename(backup, st.Hook); err != nil {
			return st, err
		}
		st.State = StateForeign
	}
	return st, nil
}

// backupPath picks a name for a displaced hook that does not overwrite an
// earlier one.
func backupPath(hook string) (string, error) {
	candidate := hook + ".poppy-backup"
	for i := 0; i < 100; i++ {
		if i > 0 {
			candidate = fmt.Sprintf("%s.poppy-backup.%d", hook, i)
		}
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free backup name beside %s", hook)
}

// hookPath asks git where hooks live, which respects core.hooksPath and
// resolves a linked worktree to the hooks directory it shares with its parent.
func hookPath(ctx context.Context, repo string) (string, error) {
	dir, err := gitx.New(repo).Run(ctx, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo, dir)
	}
	return filepath.Join(dir, "pre-commit"), nil
}

func versionLine() string { return fmt.Sprintf("# %s (v%d)", marker, Version) }

// script renders the hook. It fails open when poppy is not on PATH: a machine
// that has had poppy uninstalled should not have every commit in every
// repository start failing, and the warning says plainly that nothing was
// checked.
func script() string {
	fallback := ""
	if exe, err := os.Executable(); err == nil {
		q := shellQuote(exe)
		fallback = fmt.Sprintf("if [ -z \"$poppy\" ] && [ -x %s ]; then\n\tpoppy=%s\nfi\n", q, q)
	}
	return fmt.Sprintf(`#!/bin/sh
%s
# Blocks a commit whose staged content matches poppy's secret rules.
# Reinstall with: poppy hooks install --force
# Remove with:    poppy hooks uninstall

poppy=$(command -v poppy 2>/dev/null || true)
%sif [ -z "$poppy" ]; then
	echo "poppy: not found on PATH; staged content was not scanned" >&2
	exit 0
fi

exec "$poppy" secrets --staged
`, versionLine(), fallback)
}

// shellQuote renders a path as a single-quoted shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
