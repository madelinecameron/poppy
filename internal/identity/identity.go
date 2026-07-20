// Package identity derives the stable keys poppy uses to correlate
// repositories across machines.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// IDLen is the hex length of a derived id.
const IDLen = 12

// RepoID identifies a project across machines. It is derived from the
// canonical remote URL so clones at different paths on different machines
// resolve to the same id. Repositories without a remote fall back to a
// path-derived id, which is necessarily machine-local.
func RepoID(remote, absPath string) string {
	if remote == "" {
		return hash("path:" + absPath)
	}
	return hash("remote:" + CanonicalRemote(remote))
}

// PathID identifies one working tree on one machine. It keys state documents
// so that several clones or worktrees of the same project never collide.
func PathID(machine, absPath string) string {
	return hash("wt:" + machine + ":" + absPath)
}

// CanonicalRemote normalizes a remote URL so that ssh, https, and scp-style
// forms of the same repository compare equal.
func CanonicalRemote(remote string) string {
	s := strings.TrimSpace(remote)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")

	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if strings.Contains(s, "@") && strings.Contains(s, ":") {
		// scp-style: git@github.com:owner/repo
		s = strings.Replace(s, ":", "/", 1)
	}

	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}

	host, path, ok := strings.Cut(s, "/")
	if !ok {
		return strings.ToLower(s)
	}
	host, _, _ = strings.Cut(host, ":") // drop any port
	return strings.ToLower(host) + "/" + strings.Trim(path, "/")
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:IDLen]
}
