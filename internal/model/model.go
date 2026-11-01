// Package model defines the documents poppy stores in the hub repository.
package model

import (
	"sort"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/hubcheck"
	"github.com/madelinecameron/poppy/internal/secrets"
)

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

	// Daemons and WorkplaceProtocolsRoles are the machine's capability advertisement: what
	// tools run here and what protocol roles it plays. Aggregated across
	// machines, they answer "what is out there" as a read over published state,
	// with no central coordinator to query.
	Daemons                 []Daemon `json:"daemons,omitempty"`
	WorkplaceProtocolsRoles []string `json:"workplaceProtocolsRoles,omitempty"`

	// Hub is this machine's most recent look at the shared hub repository's
	// files, on the host that holds them. It is recorded even when the check
	// did not run, because a missing section and a clean one are the two
	// things this whole check exists to keep apart.
	//
	// A nil Hub means the document was written by a poppy from before the
	// check existed. That is not health either, and Result.Verdict says so.
	Hub *hubcheck.Result `json:"hub,omitempty"`
}

// Daemon is one long-running tool advertised by a machine.
type Daemon struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Role    string `json:"role,omitempty"`
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
	Claim *Claim `json:"claim,omitempty"`
	// Secrets is the most recent credential scan of this working tree, its
	// stashes, and its unpushed commits. Findings are locations only: a
	// repository, a path, a rule, and a line. The matched value is never
	// recorded, here or anywhere else, because this document is committed to a
	// hub shared across the fleet.
	Secrets   *secrets.Report `json:"secrets,omitempty"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Seq       uint64          `json:"seq"`
	// Broken marks a directory that looks like a working tree but cannot be
	// read as one, most often an orphaned worktree whose administrative
	// directory is gone. Its contents are invisible to git, so any work left
	// there is easy to lose without anyone noticing.
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

// SecretCount reports how many credential findings the last scan recorded.
func (r Repo) SecretCount() int { return r.Secrets.Count() }

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

// MachineState pairs a manifest with that machine's repositories and harness
// sessions.
type MachineState struct {
	Manifest Manifest
	Repos    []Repo
	Sessions []Session
	Deploys  []Deploy
	Live     bool
}

// Stale reports whether the machine's heartbeat is older than d.
func (m MachineState) Stale(now time.Time, d time.Duration) bool {
	return now.Sub(m.Manifest.Heartbeat) > d
}

// HubReport is one machine's account of the shared hub repository.
type HubReport struct {
	Machine string           `json:"machine"`
	Result  *hubcheck.Result `json:"result"`
	// Stale marks a report too old to speak for the hub now: the machine has
	// stopped publishing, or its last check is older than the fleet allows.
	// It is still listed so a reader can see it, but it is counted as silent
	// and has no say in the verdict.
	Stale bool `json:"stale,omitempty"`
	// Superseded marks a check of the same repository on the same host that a
	// newer check from another machine replaced. Only the newest look at a
	// repository decides what is in it now.
	Superseded bool `json:"superseded,omitempty"`
}

// Verdict is what this machine's check amounts to, including the case where
// the machine recorded no check at all.
func (h HubReport) Verdict() hubcheck.Status { return h.Result.Verdict() }

// HubIntegrity is the fleet's view of the one hub repository every publishing
// machine writes to.
type HubIntegrity struct {
	// Verdict is clean only when some machine's check actually ran and found
	// nothing. It is never clean because nobody looked.
	Verdict hubcheck.Status `json:"verdict"`
	// LastRan is the most recent check that actually examined the repository,
	// and Ran counts how many machines managed it this cycle.
	LastRan time.Time `json:"lastRan,omitzero"`
	Ran     int       `json:"ran"`
	// Silent counts machines that should be checking and are not: a failed
	// ssh, a skipped fsck, a document with no check in it, a report too old
	// to count. This number is the answer to "is a clean report a real one",
	// so it is reported next to the verdict and never folded into it.
	Silent int `json:"silent"`
	// Reports carries every machine's account, so a reader can see which
	// machines are behind a verdict and which are saying nothing.
	Reports []HubReport `json:"reports,omitempty"`
}

// HubIntegrity assembles the fleet's view of the hub repository as of now.
//
// A report counts only while it is current: the machine's heartbeat and the
// check itself are both within maxAge of now. A machine that went away with a
// "found" on its last document would otherwise hold the verdict at found
// forever, whatever every live machine has seen since. Older reports are
// listed as stale and counted as silent. A maxAge of zero or less keeps every
// report current.
//
// Among current checks that ran, only the newest per host and repository
// decides the verdict. The verdict is found when any repository's newest
// check found corruption.
//
// Machines with no hub to check are counted as neither answering nor silent: a
// local-only poppy is not failing at something it was never asked to do.
func (f Fleet) HubIntegrity(now time.Time, maxAge time.Duration) HubIntegrity {
	h := HubIntegrity{Verdict: hubcheck.StatusUnknown}
	var worstAbsence hubcheck.Status

	type target struct{ host, repo string }
	newest := map[target]int{}

	for _, m := range f.Machines {
		report := HubReport{Machine: m.Manifest.Machine, Result: m.Manifest.Hub}
		verdict := report.Verdict()
		if verdict == hubcheck.StatusNotConfigured {
			continue
		}
		report.Stale = maxAge > 0 && (m.Stale(now, maxAge) ||
			(report.Result != nil && now.Sub(report.Result.CheckedAt) > maxAge))
		h.Reports = append(h.Reports, report)

		switch {
		case report.Stale:
			h.Silent++
		case verdict.Ran():
			h.Ran++
			if report.Result.CheckedAt.After(h.LastRan) {
				h.LastRan = report.Result.CheckedAt
			}
			key := target{report.Result.Host, report.Result.Repo}
			i, seen := newest[key]
			switch {
			case !seen:
				newest[key] = len(h.Reports) - 1
			case report.Result.CheckedAt.After(h.Reports[i].Result.CheckedAt):
				h.Reports[i].Superseded = true
				newest[key] = len(h.Reports) - 1
			default:
				h.Reports[len(h.Reports)-1].Superseded = true
			}
		default:
			h.Silent++
			if absenceRank(verdict) > absenceRank(worstAbsence) {
				worstAbsence = verdict
			}
		}
	}

	for _, i := range newest {
		switch h.Reports[i].Verdict() {
		case hubcheck.StatusFound:
			h.Verdict = hubcheck.StatusFound
		case hubcheck.StatusClean:
			if h.Verdict != hubcheck.StatusFound {
				h.Verdict = hubcheck.StatusClean
			}
		}
	}

	// Nothing looked. The verdict is the loudest reason nothing looked, and
	// under no circumstances is it clean.
	if h.Ran == 0 {
		h.Verdict = worstAbsence
	}
	return h
}

// absenceRank orders the ways a check can fail to happen, worst first, so the
// fleet reports the most alarming reason rather than the last one it read.
func absenceRank(s hubcheck.Status) int {
	switch s {
	case hubcheck.StatusFailed:
		return 3
	case hubcheck.StatusUnknown:
		return 2
	case hubcheck.StatusSkipped:
		return 1
	default:
		return 0
	}
}

// Session is one Claude Code or Codex harness session on one machine, and the
// repositories and branches it has worked in. Stored at
// machines/<machine>/sessions/<sessionId>.json.
//
// It is written first on the machine itself by the harness's hook, then
// published by the agent, which fills in Machine and each repository's RepoID.
type Session struct {
	Schema         int    `json:"schema"`
	Machine        string `json:"machine,omitempty"`
	Harness        string `json:"harness"`
	SessionID      string `json:"sessionId"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
	// Mailbox is the address another agent sends mail to in order to reach
	// this session. It is looked up when the session starts and is empty when
	// mailbox is not configured or did not answer.
	Mailbox   string    `json:"mailbox,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Ended is set only on an explicit end-of-session event. A Stop event is
	// not one: Claude Code fires it after every turn. A session that simply
	// went quiet is told apart by LastSeen.
	Ended     *time.Time `json:"ended,omitempty"`
	LastEvent string     `json:"lastEvent,omitempty"`
	// Repos lists every repository and branch the session has been in, in
	// the order it arrived there. A branch switch inside one repository is a
	// new entry, so a branch can name every session that touched it.
	Repos []SessionRepo `json:"repos,omitempty"`
}

// SessionRepo is one repository and branch a session has worked in.
type SessionRepo struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// RepoID correlates the repository across machines exactly as Repo.RepoID
	// does. The hook leaves it empty; the agent fills it when publishing.
	RepoID string `json:"repoId,omitempty"`
	// Branch is empty when HEAD is detached.
	Branch    string    `json:"branch,omitempty"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Heads are the commits HEAD pointed at while the session was here,
	// oldest first. They tie a session to the pull request and the deploy
	// that carried its work.
	Heads []string `json:"heads,omitempty"`
}

// Active reports whether the session has not ended and was seen within d.
func (s Session) Active(now time.Time, d time.Duration) bool {
	return s.Ended == nil && now.Sub(s.LastSeen) <= d
}

// SessionFilter narrows a fleet's sessions. Empty fields match everything.
type SessionFilter struct {
	Machine string
	// Repo matches a repository's name, case-insensitively, or its RepoID.
	Repo   string
	Branch string
	// ActiveWithin, when positive, keeps only sessions active within it.
	ActiveWithin time.Duration
	Now          time.Time
}

// Matches reports whether a session passes the filter. Repo and Branch must
// match the same entry, so "sessions on poppy's main" does not return a
// session that was on main somewhere else and in poppy on another branch.
func (f SessionFilter) Matches(s Session) bool {
	if f.Machine != "" && !strings.EqualFold(s.Machine, f.Machine) {
		return false
	}
	if f.ActiveWithin > 0 && !s.Active(f.Now, f.ActiveWithin) {
		return false
	}
	if f.Repo == "" && f.Branch == "" {
		return true
	}
	for _, r := range s.Repos {
		if f.Repo != "" && !strings.EqualFold(r.Name, f.Repo) && r.RepoID != f.Repo {
			continue
		}
		if f.Branch != "" && r.Branch != f.Branch {
			continue
		}
		return true
	}
	return false
}

// Sessions returns every session in the fleet that passes the filter, most
// recently seen first.
func (f Fleet) Sessions(filter SessionFilter) []Session {
	var out []Session
	for _, m := range f.Machines {
		for _, s := range m.Sessions {
			if filter.Matches(s) {
				out = append(out, s)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Deploy is one deploy of one service, as houseparty recorded it on the
// machine that ran it. Stored at machines/<machine>/deploys/<id>.json.
type Deploy struct {
	ID       string        `json:"id"`
	Time     time.Time     `json:"time"`
	Project  string        `json:"project"`
	Env      string        `json:"env"`
	Service  string        `json:"service"`
	Method   string        `json:"method,omitempty"`
	Host     string        `json:"host,omitempty"`
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration_ns"`
	User     string        `json:"user,omitempty"`
	Machine  string        `json:"machine,omitempty"`
	Version  string        `json:"houseparty_version,omitempty"`
	Repo     string        `json:"repo,omitempty"`
	Branch   string        `json:"branch,omitempty"`
	Revision string        `json:"revision,omitempty"`
}

// Ref is the project/env/service the deploy targeted.
func (d Deploy) Ref() string { return d.Project + "/" + d.Env + "/" + d.Service }

// Deploys returns every machine's deploys at or after since, newest first.
func (f Fleet) Deploys(since time.Time) []Deploy {
	var out []Deploy
	for _, m := range f.Machines {
		for _, d := range m.Deploys {
			if !d.Time.Before(since) {
				out = append(out, d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}
