// Package agent runs the scan-collect-publish cycle.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/madelinecameron/poppy/internal/collect"
	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/hub"
	"github.com/madelinecameron/poppy/internal/hubcheck"
	"github.com/madelinecameron/poppy/internal/mirror"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/scan"
	"github.com/madelinecameron/poppy/internal/secrets"
	"github.com/madelinecameron/poppy/internal/secretscan"
	"github.com/madelinecameron/poppy/internal/snapshot"
)

// stamped is set by the main package from an -ldflags value. The closet
// publisher builds from a source checkout rather than "go install module@vX",
// and a checkout build has no module version in its build info, so without
// this every fleet-published binary reported "dev" -- including in the machine
// record below, which is the half of the comparison `houseparty fleet` uses to
// detect version drift. It was diffing the pinned version against the constant
// string "dev" for every machine.
var stamped string

// SetVersion records the version a build was stamped with. Empty values are
// ignored so a local `go build` still falls through to the build-info path.
func SetVersion(v string) {
	if v != "" && v != "dev" {
		stamped = v
	}
}

// Version reports the agent version shown in manifests and by "poppy version".
// Precedence: the linker stamp, then the module version that "go install
// module@vX" embeds in build info, then "dev" for a plain local build.
func Version() string {
	if stamped != "" {
		return stamped
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

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
	// Secrets counts repositories holding at least one credential finding, and
	// Findings the findings themselves.
	Secrets  int
	Findings int
	// Hub is what this cycle's look at the hub repository found. It is never
	// nil: a cycle that did not check still says so.
	Hub      *hubcheck.Result
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
	prevManifest, _ := a.Store.ReadManifest(a.Config.Machine)

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

	a.scanSecrets(ctx, repos, prev)
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

	hubResult := a.checkHub(ctx, prevManifest.Hub)

	manifest := model.Manifest{
		Schema:                  model.SchemaVersion,
		Machine:                 a.Config.Machine,
		OS:                      runtime.GOOS,
		Arch:                    runtime.GOARCH,
		Version:                 Version(),
		ScanRoots:               roots,
		RepoCount:               len(repos),
		Seq:                     seq,
		Heartbeat:               time.Now().UTC(),
		ScanMillis:              time.Since(start).Milliseconds(),
		Daemons:                 installedTools(ctx, a.Config.WorkplaceProtocolsPath),
		WorkplaceProtocolsRoles: a.Config.WorkplaceProtocolsRoles,
		Hub:                     hubResult,
	}

	pushed, pubErr := a.Store.Publish(ctx, manifest, repos)

	report := Report{
		Repos:     len(repos),
		Snapshots: snapshots,
		Pruned:    pruned,
		Synced:    synced,
		Hub:       hubResult,
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
		if n := r.SecretCount(); n > 0 {
			report.Secrets++
			report.Findings += n
		}
	}
	return report, nil
}

// checkHub looks at the hub repository's files on the host that holds them.
//
// It never returns nil and never returns an error. Whatever happened — the
// check ran, the host was unreachable, this machine has no hub — comes back as
// a Result whose status says which, because the whole point of this check is
// that its absence must be as visible as its findings.
func (a *Agent) checkHub(ctx context.Context, prev *hubcheck.Result) *hubcheck.Result {
	if !a.Config.HubCheck.Enabled {
		return hubcheck.NotRun(hubcheck.StatusSkipped, "hubCheck.enabled is false in this machine's configuration")
	}

	opts, reason := a.Config.HubCheckTarget()
	if reason != "" {
		return hubcheck.NotRun(hubcheck.StatusNotConfigured, reason)
	}

	opts.Fsck, opts.FsckSkipReason = fsckDue(prev, a.Config.HubCheck.FsckInterval(), time.Now())
	if prev != nil {
		opts.PrevBoot = prev.HostBoot
	}

	result := hubcheck.Run(ctx, opts)
	switch {
	case result.Corrupt():
		a.Log.Error("hub repository is corrupt",
			"repo", result.Repo, "host", result.Host,
			"zeroLengthObjects", result.ZeroCount, "reason", result.Reason)
	case !result.Sweep.Status.Ran():
		// Not a warning. A check that did not run is the state that let the
		// last outage go unnoticed for sixteen days.
		a.Log.Error("hub repository was not checked",
			"repo", result.Repo, "host", result.Host,
			"sweep", result.Sweep.Status, "fsck", result.Fsck.Status,
			"reason", result.Reason)
	}
	return result
}

// fsckDue decides whether this cycle runs the full fsck, and says why not when
// it does not.
//
// Three cases run one regardless of the interval: no previous result, a
// previous fsck that did not itself run, and a previous fsck that found
// something. The last matters most — a hub already known to be broken is
// re-examined every cycle, so the finding cannot quietly become a skip.
func fsckDue(prev *hubcheck.Result, interval time.Duration, now time.Time) (bool, string) {
	if interval <= 0 || prev == nil || !prev.Fsck.Status.Ran() || prev.Fsck.Status == hubcheck.StatusFound {
		return true, ""
	}
	if since := now.Sub(prev.CheckedAt); since < interval {
		return false, fmt.Sprintf("the last full fsck was %s ago; hubCheck.fsckIntervalSeconds is %s",
			since.Round(time.Second), interval)
	}
	return true, ""
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

// scanSecrets records where credentials sit in each repository: in the working
// tree, in stashes, and in commits on branches no remote has. The last two are
// the reason this lives in poppy at all — they are the content that exists on
// exactly one disk, that no pre-commit hook will ever see, and that only
// something enumerating every repository on every machine can find.
//
// A previous result is carried forward when the repository's git state has not
// moved and the result is younger than the configured interval. Without that,
// a five-minute cycle would re-read every file of every repository all day.
func (a *Agent) scanSecrets(ctx context.Context, repos []model.Repo, prev map[string]model.Repo) {
	if !a.Config.Secrets.Enabled {
		return
	}

	prevByPath := make(map[string]model.Repo, len(prev))
	for _, r := range prev {
		prevByPath[r.Path] = r
	}

	opts := a.Config.Secrets.ScanOptions()
	interval := a.Config.Secrets.Interval()
	now := time.Now()

	var wg sync.WaitGroup
	sem := make(chan struct{}, collectWorkers)

	for i := range repos {
		r := &repos[i]
		if r.Broken || a.Config.Secrets.ExcludesRepo(r.Path) {
			continue
		}
		fingerprint := secretscan.Fingerprint(*r)
		if old, ok := prevByPath[r.Path]; ok && reusable(old.Secrets, fingerprint, now, interval) {
			r.Secrets = old.Secrets
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			report := secretscan.Repo(ctx, r.Path, opts)
			report.Fingerprint = fingerprint
			if report.Error != "" {
				a.Log.Warn("secret scan degraded", "repo", r.Name, "err", report.Error)
			}
			r.Secrets = &report
		}()
	}
	wg.Wait()
}

// reusable reports whether a previous scan still describes the repository.
func reusable(prev *secrets.Report, fingerprint string, now time.Time, interval time.Duration) bool {
	return prev != nil &&
		prev.Fingerprint == fingerprint &&
		now.Sub(prev.ScannedAt) < interval
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
		"hub", r.Hub.Verdict(),
		"dirty", r.Dirty,
		"unpushed", r.Unpushed,
		"snapshots", r.Snapshots,
		"pruned", r.Pruned,
		"synced", r.Synced,
		"secrets", r.Findings,
		"took", r.Duration.Round(time.Millisecond),
		"pushed", r.Pushed,
	}
	if r.Err != nil {
		a.Log.Warn("published locally, hub unreachable", append(attrs, "err", r.Err)...)
		return
	}
	a.Log.Info("scan complete", attrs...)
}
