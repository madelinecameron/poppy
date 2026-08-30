package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/secrets"
	"github.com/madelinecameron/poppy/internal/snapshot"
)

// registry lists the read-only tools the server exposes.
func registry() []tool {
	noArgs := map[string]any{"type": "object", "properties": map[string]any{}}
	readOnly := func() *toolAnnotations {
		return &toolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	}

	return []tool{
		{
			descriptor: toolDescriptor{
				Name:        "fleet_status",
				Description: "Overview of every machine in the fleet: last seen, and how many repositories are dirty, unpushed, or have off-machine snapshots.",
				InputSchema: noArgs,
				Annotations: readOnly(),
			},
			handle: fleetStatus,
		},
		{
			descriptor: toolDescriptor{
				Name:        "dirty_trees",
				Description: "Every working tree with uncommitted changes anywhere in the fleet, oldest first, with which machine holds it and whether it is backed up.",
				InputSchema: noArgs,
				Annotations: readOnly(),
			},
			handle: dirtyTrees,
		},
		{
			descriptor: toolDescriptor{
				Name:        "find_work",
				Description: "Locate a repository or branch across the fleet by name or keyword. Answers 'where is this branch?' and 'which machine has this project?'.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "Repository name, branch name, or keyword to search for.",
						},
					},
					"required": []string{"query"},
				},
				Annotations: readOnly(),
			},
			handle: findWork,
		},
		{
			descriptor: toolDescriptor{
				Name:        "repo_state",
				Description: "The full recorded state of one repository on one machine: branches, tracking, dirty files, stashes, and latest snapshot.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"machine": map[string]any{"type": "string", "description": "Machine name."},
						"repo":    map[string]any{"type": "string", "description": "Repository name."},
					},
					"required": []string{"machine", "repo"},
				},
				Annotations: readOnly(),
			},
			handle: repoState,
		},
		{
			descriptor: toolDescriptor{
				Name:        "snapshot_history",
				Description: "Time-machine listing of captured snapshots for a repository. Full history is available for repositories checked out on this machine.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"machine": map[string]any{"type": "string", "description": "Machine name."},
						"repo":    map[string]any{"type": "string", "description": "Repository name."},
						"branch":  map[string]any{"type": "string", "description": "Optional branch to restrict to."},
					},
					"required": []string{"machine", "repo"},
				},
				Annotations: readOnly(),
			},
			handle: snapshotHistory,
		},
		{
			descriptor: toolDescriptor{
				Name: "secret_findings",
				Description: "Credentials detected across the fleet, as locations only — repository, path, rule, and line. " +
					"Includes findings in stashes and in commits on branches no remote has, which exist on one machine and are invisible to a pre-commit hook. " +
					"The matched value is never recorded and cannot be retrieved.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"machine": map[string]any{"type": "string", "description": "Optional machine to restrict to."},
						"rule":    map[string]any{"type": "string", "description": "Optional rule id to restrict to, such as github-token."},
						"origin": map[string]any{
							"type":        "string",
							"description": "Optional origin to restrict to: worktree, stash, or commit.",
						},
					},
				},
				Annotations: readOnly(),
			},
			handle: secretFindings,
		},
		{
			descriptor: toolDescriptor{
				Name:        "where_was_i",
				Description: "A narrative digest of recent activity across the fleet: what was touched, what advanced, and what is still uncommitted. Answers 'what was I working on?'.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"machine": map[string]any{"type": "string", "description": "Optional machine to focus on."},
					},
				},
				Annotations: readOnly(),
			},
			handle: whereWasI,
		},
	}
}

func fleetStatus(ctx context.Context, s *Server, _ map[string]any) (string, error) {
	fleet, err := s.fleet(ctx)
	if err != nil {
		return "", err
	}
	now := time.Now()
	staleAfter := time.Duration(s.cfg.StaleAfterSeconds) * time.Second

	type machineView struct {
		Machine   string    `json:"machine"`
		OS        string    `json:"os"`
		Arch      string    `json:"arch"`
		LastSeen  time.Time `json:"lastSeen"`
		Stale     bool      `json:"stale"`
		Repos     int       `json:"repos"`
		Dirty     int       `json:"dirty"`
		Unpushed  int       `json:"unpushed"`
		Snapshots int       `json:"snapshotsSynced"`
		Secrets   int       `json:"secretFindings"`
	}
	type totals struct {
		Machines int `json:"machines"`
		Repos    int `json:"repos"`
		Dirty    int `json:"dirty"`
		Unpushed int `json:"unpushed"`
		Secrets  int `json:"secretFindings"`
	}
	out := struct {
		Machines []machineView `json:"machines"`
		Totals   totals        `json:"totals"`
	}{}

	for _, m := range fleet.Machines {
		mv := machineView{
			Machine:  m.Manifest.Machine,
			OS:       m.Manifest.OS,
			Arch:     m.Manifest.Arch,
			LastSeen: m.Manifest.Heartbeat,
			Stale:    m.Stale(now, staleAfter),
			Repos:    len(m.Repos),
		}
		for _, r := range m.Repos {
			if r.Dirty.Any() {
				mv.Dirty++
			}
			if r.Unpushed() {
				mv.Unpushed++
			}
			if r.Snapshot != nil && r.Snapshot.Synced {
				mv.Snapshots++
			}
			mv.Secrets += r.SecretCount()
		}
		out.Machines = append(out.Machines, mv)
		out.Totals.Repos += mv.Repos
		out.Totals.Dirty += mv.Dirty
		out.Totals.Unpushed += mv.Unpushed
		out.Totals.Secrets += mv.Secrets
	}
	out.Totals.Machines = len(fleet.Machines)
	return jsonText(out)
}

func dirtyTrees(ctx context.Context, s *Server, _ map[string]any) (string, error) {
	fleet, err := s.fleet(ctx)
	if err != nil {
		return "", err
	}
	now := time.Now()

	type entry struct {
		Machine    string     `json:"machine"`
		Repo       string     `json:"repo"`
		Branch     string     `json:"branch"`
		Path       string     `json:"path"`
		DirtySince *time.Time `json:"dirtySince,omitempty"`
		AgeHours   float64    `json:"ageHours"`
		Staged     int        `json:"staged"`
		Unstaged   int        `json:"unstaged"`
		Untracked  int        `json:"untracked"`
		BackedUp   bool       `json:"backedUp"`
	}
	var entries []entry

	for _, m := range fleet.Machines {
		for _, r := range m.Repos {
			if !r.Dirty.Any() {
				continue
			}
			e := entry{
				Machine: m.Manifest.Machine, Repo: r.Name, Branch: r.Head.Branch, Path: r.Path,
				DirtySince: r.DirtySince,
				Staged:     r.Dirty.Staged, Unstaged: r.Dirty.Unstaged, Untracked: r.Dirty.Untracked,
				BackedUp: r.Snapshot != nil && r.Snapshot.Synced,
			}
			if r.DirtySince != nil {
				e.AgeHours = round1(now.Sub(*r.DirtySince).Hours())
			}
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].AgeHours > entries[j].AgeHours })

	return jsonText(struct {
		Count int     `json:"count"`
		Dirty []entry `json:"dirty"`
	}{Count: len(entries), Dirty: entries})
}

// secretFindings reports where credentials are, and only where. There is no
// tool that returns a matched value, because no such value is stored: the
// answer to "what is the secret" is "look at the file yourself".
func secretFindings(ctx context.Context, s *Server, args map[string]any) (string, error) {
	machine := argString(args, "machine")
	rule := argString(args, "rule")
	origin := argString(args, "origin")

	fleet, err := s.fleet(ctx)
	if err != nil {
		return "", err
	}

	type entry struct {
		Machine  string `json:"machine"`
		Repo     string `json:"repo"`
		RepoPath string `json:"repoPath"`
		Origin   string `json:"origin"`
		Ref      string `json:"ref,omitempty"`
		Path     string `json:"path"`
		Line     int    `json:"line"`
		Rule     string `json:"rule"`
		// Unreachable marks a finding in content no remote has: a stash, or a
		// commit on a branch that was never pushed. It exists on one machine.
		Unreachable bool `json:"unreachable"`
	}
	entries := []entry{}
	skipped := []string{}

	for _, m := range fleet.Machines {
		if machine != "" && m.Manifest.Machine != machine {
			continue
		}
		for _, r := range m.Repos {
			for _, sk := range r.Secrets.Skips() {
				skipped = append(skipped, m.Manifest.Machine+":"+r.Name+" — "+sk.Explain())
			}
			for _, f := range r.Secrets.All() {
				if rule != "" && string(f.Rule()) != rule {
					continue
				}
				if origin != "" && string(f.Origin()) != origin {
					continue
				}
				entries = append(entries, entry{
					Machine: m.Manifest.Machine, Repo: r.Name, RepoPath: r.Path,
					Origin: string(f.Origin()), Ref: f.Ref(), Path: f.Path(),
					Line: f.Line(), Rule: string(f.Rule()),
					Unreachable: f.Origin() == secrets.OriginStash || f.Origin() == secrets.OriginCommit,
				})
			}
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Unreachable != entries[j].Unreachable {
			return entries[i].Unreachable
		}
		if entries[i].Machine != entries[j].Machine {
			return entries[i].Machine < entries[j].Machine
		}
		return entries[i].Repo < entries[j].Repo
	})

	unreachable := 0
	for _, e := range entries {
		if e.Unreachable {
			unreachable++
		}
	}

	return jsonText(struct {
		Count int `json:"count"`
		// Unreachable is the count that no remote has and no hook can reach.
		Unreachable int      `json:"unreachable"`
		Findings    []entry  `json:"findings"`
		NotScanned  []string `json:"notScanned,omitempty"`
		Note        string   `json:"note"`
	}{
		Count: len(entries), Unreachable: unreachable, Findings: entries,
		NotScanned: skipped,
		Note:       "locations only; poppy never records the matched value",
	})
}

func findWork(ctx context.Context, s *Server, args map[string]any) (string, error) {
	query := strings.ToLower(strings.TrimSpace(argString(args, "query")))
	if query == "" {
		return "", fmt.Errorf("find_work needs a non-empty query")
	}
	fleet, err := s.fleet(ctx)
	if err != nil {
		return "", err
	}

	type match struct {
		Machine   string   `json:"machine"`
		Repo      string   `json:"repo"`
		Path      string   `json:"path"`
		MatchedOn string   `json:"matchedOn"`
		Branches  []string `json:"branches,omitempty"`
		Dirty     bool     `json:"dirty"`
		Head      string   `json:"head"`
	}
	matches := []match{}

	for _, m := range fleet.Machines {
		for _, r := range m.Repos {
			var branchHits []string
			for _, b := range r.Branches {
				if strings.Contains(strings.ToLower(b.Name), query) {
					branchHits = append(branchHits, b.Name)
				}
			}
			matchedOn := ""
			switch {
			case strings.Contains(strings.ToLower(r.Name), query):
				matchedOn = "repo"
			case len(branchHits) > 0:
				matchedOn = "branch"
			case containsRemote(r, query):
				matchedOn = "remote"
			}
			if matchedOn == "" {
				continue
			}
			matches = append(matches, match{
				Machine: m.Manifest.Machine, Repo: r.Name, Path: r.Path,
				MatchedOn: matchedOn, Branches: branchHits,
				Dirty: r.Dirty.Any(), Head: r.Head.Branch,
			})
		}
	}

	return jsonText(struct {
		Query   string  `json:"query"`
		Count   int     `json:"count"`
		Matches []match `json:"matches"`
	}{Query: query, Count: len(matches), Matches: matches})
}

func repoState(ctx context.Context, s *Server, args map[string]any) (string, error) {
	machine := argString(args, "machine")
	repo := argString(args, "repo")
	if machine == "" || repo == "" {
		return "", fmt.Errorf("repo_state needs both machine and repo")
	}
	fleet, err := s.fleet(ctx)
	if err != nil {
		return "", err
	}
	r, ok := findRepo(fleet, machine, repo)
	if !ok {
		return "", fmt.Errorf("no repository named %q on machine %q", repo, machine)
	}
	return jsonText(r)
}

func snapshotHistory(ctx context.Context, s *Server, args map[string]any) (string, error) {
	machine := argString(args, "machine")
	repo := argString(args, "repo")
	branch := argString(args, "branch")
	if machine == "" || repo == "" {
		return "", fmt.Errorf("snapshot_history needs both machine and repo")
	}
	fleet, err := s.fleet(ctx)
	if err != nil {
		return "", err
	}
	r, ok := findRepo(fleet, machine, repo)
	if !ok {
		return "", fmt.Errorf("no repository named %q on machine %q", repo, machine)
	}

	type snapView struct {
		SHA      string    `json:"sha"`
		Branch   string    `json:"branch"`
		Taken    time.Time `json:"taken"`
		AgeHours float64   `json:"ageHours"`
	}
	now := time.Now()
	out := struct {
		Machine   string     `json:"machine"`
		Repo      string     `json:"repo"`
		Local     bool       `json:"local"`
		Note      string     `json:"note,omitempty"`
		Snapshots []snapView `json:"snapshots"`
	}{Machine: machine, Repo: repo, Snapshots: []snapView{}}

	// Full history lives inside the repository. When the repository is checked
	// out on this machine, list it directly; otherwise the fleet state records
	// only the latest snapshot, so report that and say why.
	if machine == s.cfg.Machine {
		snaps, err := snapshot.List(ctx, r.Path, machine, branch)
		if err != nil {
			return "", fmt.Errorf("read snapshots: %w", err)
		}
		out.Local = true
		for _, snap := range snaps {
			out.Snapshots = append(out.Snapshots, snapView{
				SHA: snap.SHA, Branch: snap.Branch, Taken: snap.Taken,
				AgeHours: round1(now.Sub(snap.Taken).Hours()),
			})
		}
		return jsonText(out)
	}

	out.Note = "full history is only available on " + machine + "; showing the latest snapshot recorded in fleet state"
	if r.Snapshot != nil {
		out.Snapshots = append(out.Snapshots, snapView{
			SHA: r.Snapshot.SHA, Branch: r.Snapshot.Branch, Taken: r.Snapshot.Taken,
			AgeHours: round1(now.Sub(r.Snapshot.Taken).Hours()),
		})
	}
	return jsonText(out)
}

func whereWasI(ctx context.Context, s *Server, args map[string]any) (string, error) {
	focus := argString(args, "machine")
	fleet, err := s.fleet(ctx)
	if err != nil {
		return "", err
	}
	now := time.Now()

	var b strings.Builder
	wrote := false
	for _, m := range fleet.Machines {
		if focus != "" && m.Manifest.Machine != focus {
			continue
		}
		active := recentlyActive(m.Repos, now)
		if len(active) == 0 {
			continue
		}
		wrote = true
		fmt.Fprintf(&b, "On %s (last seen %s):\n", m.Manifest.Machine, relative(now.Sub(m.Manifest.Heartbeat)))
		for _, r := range active {
			fmt.Fprintf(&b, "  %s\n", describeActivity(r, now))
		}
		b.WriteString("\n")
	}

	if !wrote {
		if focus != "" {
			return fmt.Sprintf("No recent activity recorded on %s.", focus), nil
		}
		return "No recent activity recorded across the fleet.", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// recentlyActive returns a machine's repositories that hold work in progress or
// were committed to recently, most recent first, capped so the digest stays
// readable.
func recentlyActive(repos []model.Repo, now time.Time) []model.Repo {
	type scored struct {
		repo model.Repo
		when time.Time
	}
	var items []scored
	for _, r := range repos {
		if r.Broken {
			continue
		}
		when := time.Time{}
		if r.DirtySince != nil {
			when = *r.DirtySince
		}
		if r.LastCommit != nil && r.LastCommit.When.After(when) {
			when = r.LastCommit.When
		}
		if r.Dirty.Any() || r.Unpushed() || (r.LastCommit != nil && now.Sub(r.LastCommit.When) < 7*24*time.Hour) {
			items = append(items, scored{r, when})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].when.After(items[j].when) })

	const maxRepos = 10
	if len(items) > maxRepos {
		items = items[:maxRepos]
	}
	out := make([]model.Repo, len(items))
	for i, it := range items {
		out[i] = it.repo
	}
	return out
}

func describeActivity(r model.Repo, now time.Time) string {
	parts := []string{r.Name}
	if r.Head.Branch != "" {
		parts[0] = fmt.Sprintf("%s on %s", r.Name, r.Head.Branch)
	}

	var notes []string
	if r.Dirty.Any() {
		age := ""
		if r.DirtySince != nil {
			age = " for " + relative(now.Sub(*r.DirtySince))
		}
		notes = append(notes, fmt.Sprintf("%d uncommitted change(s)%s", r.Dirty.Total(), age))
	}
	if n := countUnpushed(r); n > 0 {
		notes = append(notes, fmt.Sprintf("%d unpushed commit(s)", n))
	}
	if r.LastCommit != nil {
		notes = append(notes, fmt.Sprintf("last commit %q %s", r.LastCommit.Subject, relative(now.Sub(r.LastCommit.When))))
	}
	if len(notes) == 0 {
		return parts[0]
	}
	return parts[0] + " — " + strings.Join(notes, ", ")
}

func countUnpushed(r model.Repo) int {
	total := 0
	for _, b := range r.UnpushedBranches() {
		total += b.Ahead
	}
	return total
}

func findRepo(fleet model.Fleet, machine, repo string) (model.Repo, bool) {
	want := strings.ToLower(repo)
	for _, m := range fleet.Machines {
		if m.Manifest.Machine != machine {
			continue
		}
		for _, r := range m.Repos {
			if strings.ToLower(r.Name) == want {
				return r, true
			}
		}
	}
	return model.Repo{}, false
}

func containsRemote(r model.Repo, query string) bool {
	for _, url := range r.Remotes {
		if strings.Contains(strings.ToLower(url), query) {
			return true
		}
	}
	return false
}

func round1(f float64) float64 {
	if f < 0 {
		f = 0
	}
	return float64(int(f*10+0.5)) / 10
}

func relative(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
