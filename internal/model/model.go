// Package model defines the documents poppy stores in the hub repository.
package model

import "time"

// SchemaVersion is bumped when a document shape changes incompatibly.
const SchemaVersion = 1

// Manifest describes one machine in the fleet. Stored at
// machines/<machine>/manifest.json.
type Manifest struct {
	Schema     int       `json:"schema"`
	Machine    string    `json:"machine"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	Version    string    `json:"version"`
	ScanRoots  []string  `json:"scanRoots"`
	RepoCount  int       `json:"repoCount"`
	Seq        uint64    `json:"seq"`
	Heartbeat  time.Time `json:"heartbeat"`
	ScanMillis int64     `json:"scanMillis"`
}

// Repo is the state of a single repository on a single machine. Stored at
// machines/<machine>/repos/<pathID>.json.
//
// RepoID is derived from the canonical remote URL so the same project
// correlates across machines; the file is keyed by a hash of the absolute
// path so multiple clones and worktrees on one machine never collide.
type Repo struct {
	Schema     int               `json:"schema"`
	RepoID     string            `json:"repoId"`
	PathID     string            `json:"pathId"`
	Machine    string            `json:"machine"`
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Remotes    map[string]string `json:"remotes,omitempty"`
	Head       Head              `json:"head"`
	Branches   []Branch          `json:"branches,omitempty"`
	Dirty      Dirty             `json:"dirty"`
	DirtySince *time.Time        `json:"dirtySince,omitempty"`
	Stashes    int               `json:"stashes"`
	IsWorktree bool              `json:"isWorktree,omitempty"`
	Worktrees  []string          `json:"worktrees,omitempty"`
	LastCommit *Commit           `json:"lastCommit,omitempty"`
	LastFetch  *time.Time        `json:"lastFetch,omitempty"`
	// Snapshot is this working tree's most recent captured state, if any. It
	// is what another machine fetches when it teleports the work with grab.
	Snapshot *SnapshotRef `json:"snapshot,omitempty"`
	// Claim records that another machine has teleported this work and is now
	// its live copy, so edits made here after the claim are flagged as a
	// divergence rather than silently competing.
	Claim     *Claim    `json:"claim,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Seq       uint64    `json:"seq"`
	// Broken marks a directory that looks like a working tree but cannot be
	// read as one, most often an orphaned worktree whose administrative
	// directory is gone. Its contents are invisible to git, so any work left
	// there is stranded in the strongest sense.
	Broken bool   `json:"broken,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Head is the checked-out position. Branch is empty when detached.
type Head struct {
	Branch   string `json:"branch,omitempty"`
	SHA      string `json:"sha,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Unborn   bool   `json:"unborn,omitempty"`
}

// Branch is a local branch and its relationship to its upstream.
type Branch struct {
	Name     string `json:"name"`
	SHA      string `json:"sha"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Gone     bool   `json:"gone,omitempty"`
}

// Unpushed reports whether the branch holds commits that exist nowhere else.
func (b Branch) Unpushed() bool { return b.Ahead > 0 || (b.Upstream == "" && b.SHA != "") }

// HasRemote reports whether the repository can push anywhere at all.
func (r Repo) HasRemote() bool { return len(r.Remotes) > 0 }

// UnpushedBranches lists branches holding commits no remote has.
//
// A repository with no remote at all is local by construction; reporting every
// one of its branches as unpushed would drown the signal that matters, so that
// case is reported once by LocalOnly instead.
func (r Repo) UnpushedBranches() []Branch {
	if !r.HasRemote() {
		return nil
	}
	var out []Branch
	for _, b := range r.Branches {
		if b.Unpushed() {
			out = append(out, b)
		}
	}
	return out
}

// Unpushed reports whether any branch holds commits no remote has.
func (r Repo) Unpushed() bool { return len(r.UnpushedBranches()) > 0 }

// LocalOnly reports a repository with commits but nowhere to push them, which
// exists on exactly one machine and is lost with it.
func (r Repo) LocalOnly() bool { return !r.Broken && !r.HasRemote() && !r.Head.Unborn }

// AtRisk reports whether the repository holds work that exists in only one
// place: uncommitted changes, unpushed commits, stashes, no remote at all, or
// a working tree git can no longer read.
func (r Repo) AtRisk() bool {
	if r.Broken {
		return true
	}
	return r.Dirty.Any() || r.Unpushed() || r.Stashes > 0 || r.LocalOnly()
}

// Dirty counts working-tree deviations. Files lists paths only, never content.
type Dirty struct {
	Staged    int      `json:"staged"`
	Unstaged  int      `json:"unstaged"`
	Untracked int      `json:"untracked"`
	Unmerged  int      `json:"unmerged"`
	Files     []string `json:"files,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Any reports whether the working tree deviates from HEAD in any way.
func (d Dirty) Any() bool {
	return d.Staged+d.Unstaged+d.Untracked+d.Unmerged > 0
}

// Total is the number of deviating paths.
func (d Dirty) Total() int {
	return d.Staged + d.Unstaged + d.Untracked + d.Unmerged
}

// Commit is a minimal commit description.
type Commit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	When    time.Time `json:"when"`
}

// SnapshotRef points at a captured working tree.
type SnapshotRef struct {
	SHA    string    `json:"sha"`
	Ref    string    `json:"ref"`
	Branch string    `json:"branch"`
	Taken  time.Time `json:"taken"`
	// Synced reports whether the snapshot has reached the mirror and can
	// therefore be teleported to another machine.
	Synced bool `json:"synced"`
}

// Claim records a teleport of a working tree onto another machine.
type Claim struct {
	By       string    `json:"by"`
	Branch   string    `json:"branch"`
	Snapshot string    `json:"snapshot"`
	At       time.Time `json:"at"`
}

// Fleet is the aggregate view assembled from every machine's documents.
type Fleet struct {
	Machines []MachineState
}

// MachineState pairs a manifest with that machine's repositories.
type MachineState struct {
	Manifest Manifest
	Repos    []Repo
	Live     bool
}

// Stale reports whether the machine's heartbeat is older than d.
func (m MachineState) Stale(now time.Time, d time.Duration) bool {
	return now.Sub(m.Manifest.Heartbeat) > d
}
