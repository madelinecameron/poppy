// Package scan discovers git repositories beneath a set of roots.
package scan

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSkipDirs are never descended into. They are large, machine-local,
// and never contain repositories worth tracking.
var DefaultSkipDirs = []string{
	"node_modules", "vendor", ".venv", "venv", "__pycache__", ".mypy_cache",
	"target", "dist", "build", ".next", ".nuxt", ".gradle", ".terraform",
	"Library", ".Trash", ".cache", ".cargo", ".rustup", ".npm", ".pnpm-store",
	"Pods", "DerivedData", ".tox", ".pytest_cache", ".direnv",
}

// DefaultMaxDepth bounds how deep beneath a root a repository may be found.
const DefaultMaxDepth = 8

// Options controls a scan.
type Options struct {
	Roots    []string
	SkipDirs []string
	MaxDepth int
	// IncludeWorktrees reports linked worktrees as their own entries. A dirty
	// worktree strands work exactly as a dirty clone does.
	IncludeWorktrees bool
}

// Found is a discovered repository.
type Found struct {
	Path       string
	IsWorktree bool
}

// Run walks the roots and returns every repository found, sorted by path.
func Run(ctx context.Context, opts Options) ([]Found, error) {
	skip := make(map[string]bool, len(DefaultSkipDirs))
	for _, d := range DefaultSkipDirs {
		skip[d] = true
	}
	for _, d := range opts.SkipDirs {
		skip[d] = true
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}

	seen := make(map[string]bool)
	var found []Found

	for _, root := range opts.Roots {
		root = expand(root)
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		rootDepth := strings.Count(filepath.Clean(abs), string(filepath.Separator))

		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil // unreadable directories are skipped, not fatal
			}
			if !d.IsDir() {
				return nil
			}
			name := d.Name()
			if path != abs {
				if skip[name] {
					return fs.SkipDir
				}
				if strings.Count(path, string(filepath.Separator))-rootDepth > maxDepth {
					return fs.SkipDir
				}
			}

			dotgit := filepath.Join(path, ".git")
			info, err := os.Lstat(dotgit)
			if err != nil {
				return nil
			}

			isWorktree := !info.IsDir()
			if isWorktree && !opts.IncludeWorktrees {
				return fs.SkipDir
			}
			// Report the physical path. git resolves symlinks when it reports
			// a working tree's root, and state documents are keyed by path, so
			// an unresolved path here would fail to match the same repository
			// on the next scan and reset the age of any dirty work.
			repoPath := path
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				repoPath = resolved
			}
			if !seen[repoPath] {
				seen[repoPath] = true
				found = append(found, Found{Path: repoPath, IsWorktree: isWorktree})
			}
			// Nested repositories below a repository root are submodules or
			// vendored copies; poppy tracks the outer repository only.
			return fs.SkipDir
		})
		if err != nil && ctx.Err() != nil {
			return found, ctx.Err()
		}
	}
	return found, nil
}

func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// Expand makes a configured path absolute, resolving a leading ~.
func Expand(p string) string { return expand(p) }
