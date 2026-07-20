// Package agent runs the scan-collect-publish cycle.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/mads/poppy/internal/collect"
	"github.com/mads/poppy/internal/config"
	"github.com/mads/poppy/internal/hub"
	"github.com/mads/poppy/internal/mirror"
	"github.com/mads/poppy/internal/model"
	"github.com/mads/poppy/internal/scan"
	"github.com/mads/poppy/internal/snapshot"
)

// Version is the agent version reported in manifests.
const Version = "0.1.0"

// collectWorkers bounds concurrent git invocations during a scan so a large
// fleet of repositories does not saturate the machine.
var collectWorkers = min(runtime.NumCPU(), 8)

// Agent scans this machine and publishes its state.
type Agent struct {
	Config config.Config
	Store  *hub.Store
	Log    *slog.Logger
}

// Report summarizes one cycle.
type Report struct {
	Repos     int
	Dirty     int
	Unpushed  int
	Snapshots int
	Pruned    int
	Synced    int
	Duration  time.Duration
	Pushed    bool
	Err       error
}

// New returns an Agent using cfg and store.
func New(cfg config.Config, store *hub.Store, log *slog.Logger) *Agent {
	if log == nil {
		log = slog.Default()
	}
	return &Agent{Config: cfg, Store: store, Log: log}
}

// Cycle performs one scan and publishes the result.
func (a *Agent) Cycle(ctx context.Context) (Report, error) {
	start := time.Now()

	// Previous state is read before publishing, which may reset the working
	// tree, so that dirty ages survive a cycle taken while offline.
	prev := a.Store.ReadMachine(a.Config.Machine)

	found, err := scan.Run(ctx, scan.Options{
		Roots:            a.Config.Roots,
		SkipDirs:         a.Config.SkipDirs,
		MaxDepth:         a.Config.MaxDepth,
		IncludeWorktrees: a.Config.IncludeWorktrees,
	})
	if err != nil {
		return Report{Err: err}, err
	}

	repos := a.collectAll(ctx, found, prev)
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })

	snapshots, pruned := a.capture(ctx, repos)
	synced := a.sync(ctx, repos)

	seq := a.Store.NextSeq(a.Config.Machine)
	for i := range repos {
		repos[i].Seq = seq
	}

	roots := make([]string, 0, len(a.Config.Roots))
	for _, r := range a.Config.Roots {
		roots = append(roots, scan.Expand(r))
	}

	manifest := model.Manifest{
		Schema:     model.SchemaVersion,
		Machine:    a.Config.Machine,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Version:    Version,
		ScanRoots:  roots,
		RepoCount:  len(repos),
		Seq:        seq,
		Heartbeat:  time.Now().UTC(),
		ScanMillis: time.Since(start).Milliseconds(),
	}

	pushed, pubErr := a.Store.Publish(ctx, manifest, repos)

	report := Report{
		Repos:     len(repos),
		Snapshots: snapshots,
		Pruned:    pruned,
		Synced:    synced,
		Duration:  time.Since(start),
		Pushed:    pushed,
		Err:       pubErr,
	}
	for _, r := range repos {
		if r.Dirty.Any() {
			report.Dirty++
		}
		if r.Unpushed() {
			report.Unpushed++
		}
	}
	return report, nil
}

func (a *Agent) collectAll(ctx context.Context, found []scan.Found, prev map[string]model.Repo) []model.Repo {
	c := collect.New(a.Config.Machine)
	results := make([]model.Repo, len(found))

	prevByPath := make(map[string]model.Repo, len(prev))
	for _, r := range prev {
		prevByPath[r.Path] = r
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, collectWorkers)

	for i, f := range found {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var previous *model.Repo
			if p, ok := prevByPath[f.Path]; ok {
				previous = &p
			}
			results[i] = c.Repo(ctx, f.Path, f.IsWorktree, previous)
		}()
	}
	wg.Wait()

	out := results[:0]
	for _, r := range results {
		if r.Path != "" {
			out = append(out, r)
		}
	}
	return out
}

// capture snapshots every dirty working tree and prunes what retention no
// longer keeps. Capture failures are logged rather than propagated: a single
// unreadable repository must not stop the rest of the fleet being recorded.
func (a *Agent) capture(ctx context.Context, repos []model.Repo) (taken, pruned int) {
	if !a.Config.Snapshot.Enabled {
		return 0, 0
	}

	s := snapshot.New(a.Config.Machine)
	s.MaxFileBytes = a.Config.Snapshot.MaxFileBytes
	policy := a.Config.Snapshot.Policy()
	now := time.Now()

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	sem := make(chan struct{}, collectWorkers)

	for _, r := range repos {
		if r.Broken || a.Config.Snapshot.ExcludesRepo(r.Path) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if r.Dirty.Any() {
				switch snap, err := s.Create(ctx, r.Path); {
				case err == nil:
					mu.Lock()
					taken++
					mu.Unlock()
					if len(snap.Skipped) > 0 {
						a.Log.Warn("snapshot omitted oversized files",
							"repo", r.Name, "files", len(snap.Skipped))
					}
				case errors.Is(err, snapshot.ErrUnchanged), errors.Is(err, snapshot.ErrNothingToCapture):
					// Nothing moved since the last capture.
				default:
					a.Log.Warn("snapshot failed", "repo", r.Name, "err", err)
				}
			}

			n, err := snapshot.Prune(ctx, r.Path, a.Config.Machine, policy, now)
			if err != nil {
				a.Log.Warn("snapshot prune failed", "repo", r.Name, "err", err)
				return
			}
			mu.Lock()
			pruned += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	return taken, pruned
}

// sync records each repository's latest snapshot on its state document and,
// when a mirror is configured, pushes that snapshot off the machine. The
// recorded snapshot is what another machine teleports with grab; Synced marks
// whether it has actually reached the mirror and can therefore be fetched.
//
// A push failure is logged, not fatal: the snapshot is still safe locally and
// the state document still names it, so a later cycle republishes and retries.
func (a *Agent) sync(ctx context.Context, repos []model.Repo) int {
	for i := range repos {
		r := &repos[i]
		if r.Broken || r.Head.Branch == "" || !r.Dirty.Any() {
			continue
		}
		if snap, ok := snapshot.Latest(ctx, r.Path, a.Config.Machine, r.Head.Branch); ok {
			r.Snapshot = &model.SnapshotRef{
				SHA: snap.SHA, Ref: snap.Ref, Branch: snap.Branch, Taken: snap.Taken,
			}
		}
	}

	mirrorCfg := a.Config.Snapshot.Mirror.MirrorConfig()
	if !mirrorCfg.Enabled() {
		return 0
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		synced int
	)
	sem := make(chan struct{}, collectWorkers)

	for i := range repos {
		r := &repos[i]
		if r.Snapshot == nil || a.Config.Snapshot.ExcludesRepo(r.Path) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := mirror.For(mirrorCfg, r.RepoID).Push(ctx, r.Path, a.Config.Machine); err != nil {
				a.Log.Warn("snapshot sync failed", "repo", r.Name, "err", err)
				return
			}
			r.Snapshot.Synced = true
			mu.Lock()
			synced++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return synced
}

// Run performs cycles until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	interval := time.Duration(a.Config.ScanIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	a.logCycle(a.Cycle(ctx))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			a.logCycle(a.Cycle(ctx))
		}
	}
}

func (a *Agent) logCycle(r Report, err error) {
	if err != nil {
		a.Log.Error("scan failed", "err", err)
		return
	}
	attrs := []any{
		"repos", r.Repos,
		"dirty", r.Dirty,
		"unpushed", r.Unpushed,
		"snapshots", r.Snapshots,
		"pruned", r.Pruned,
		"synced", r.Synced,
		"took", r.Duration.Round(time.Millisecond),
		"pushed", r.Pushed,
	}
	if r.Err != nil {
		a.Log.Warn("published locally, hub unreachable", append(attrs, "err", r.Err)...)
		return
	}
	a.Log.Info("scan complete", attrs...)
}
