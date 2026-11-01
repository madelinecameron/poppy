package agent

import (
	"context"
	"time"

	"github.com/madelinecameron/poppy/internal/collect"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/sessions"
)

// SessionPollInterval is how often the agent checks whether a harness hook has
// recorded a change worth publishing before the next scan. The check is one
// stat of a marker file; the publish it may trigger is a hub fetch and push.
var SessionPollInterval = 15 * time.Second

// publishSessions prunes the local session store and publishes what remains,
// with each repository correlated to its RepoID. known is this machine's
// latest scan, which answers the RepoID of almost every path without a git
// call; a path outside the scan roots is resolved directly and remembered.
func (a *Agent) publishSessions(ctx context.Context, known []model.Repo) (int, error) {
	if a.SessionsDir == "" {
		return 0, nil
	}
	a.sessionsSeen = sessions.Changed(a.SessionsDir)

	if n, err := sessions.Prune(a.SessionsDir, time.Now(), sessions.PruneAfter); err != nil {
		a.Log.Warn("session prune failed", "err", err)
	} else if n > 0 {
		a.Log.Info("pruned sessions", "count", n)
	}
	records, err := sessions.List(a.SessionsDir)
	if err != nil {
		return 0, err
	}

	byPath := make(map[string]string, len(known))
	for _, r := range known {
		byPath[r.Path] = r.RepoID
	}
	if a.repoIDs == nil {
		a.repoIDs = map[string]string{}
	}
	for i := range records {
		records[i].Machine = a.Config.Machine
		for j := range records[i].Repos {
			r := &records[i].Repos[j]
			if id, ok := byPath[r.Path]; ok && id != "" {
				r.RepoID = id
				continue
			}
			id, ok := a.repoIDs[r.Path]
			if !ok {
				id = collect.RepoID(ctx, r.Path)
				a.repoIDs[r.Path] = id
			}
			r.RepoID = id
		}
	}

	_, err = a.Store.PublishSessions(ctx, a.Config.Machine, records)
	return len(records), err
}

// sessionsChanged reports whether a hook has marked a change since the last
// publish.
func (a *Agent) sessionsChanged() bool {
	return a.SessionsDir != "" && sessions.Changed(a.SessionsDir).After(a.sessionsSeen)
}

// publishChangedSessions is the between-scans path: it publishes only when a
// hook asked for it, against the repositories the last scan recorded.
func (a *Agent) publishChangedSessions(ctx context.Context) {
	if !a.sessionsChanged() {
		return
	}
	var known []model.Repo
	for _, r := range a.Store.ReadMachine(a.Config.Machine) {
		known = append(known, r)
	}
	n, err := a.publishSessions(ctx, known)
	if err != nil {
		a.Log.Warn("session publish failed", "sessions", n, "err", err)
		return
	}
	a.Log.Info("sessions published", "sessions", n)
}
