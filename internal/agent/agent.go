// Package agent runs the scan-collect-publish cycle.
package agent

import (
	"context"
	"log/slog"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/mads/poppy/internal/collect"
	"github.com/mads/poppy/internal/config"
	"github.com/mads/poppy/internal/hub"
	"github.com/mads/poppy/internal/model"
	"github.com/mads/poppy/internal/scan"
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
	Repos    int
	Dirty    int
	Unpushed int
	Duration time.Duration
	Pushed   bool
	Err      error
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
		Repos:    len(repos),
		Duration: time.Since(start),
		Pushed:   pushed,
		Err:      pubErr,
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
		"took", r.Duration.Round(time.Millisecond),
		"pushed", r.Pushed,
	}
	if r.Err != nil {
		a.Log.Warn("published locally, hub unreachable", append(attrs, "err", r.Err)...)
		return
	}
	a.Log.Info("scan complete", attrs...)
}
