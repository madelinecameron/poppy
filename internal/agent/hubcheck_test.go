package agent

import (
	"context"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/gittest"
	"github.com/madelinecameron/poppy/internal/hubcheck"
)

// scratchHub is a bare repository standing in for the fleet hub, on this
// machine, so the agent's check runs for real without ssh. The live hub is
// never touched by a test.
func scratchHub(t *testing.T) string {
	t.Helper()
	return gittest.NewBare(t, "state")
}

func TestEveryCycleRecordsWhatHappenedToTheHubCheck(t *testing.T) {
	local := scratchHub(t)

	for name, cfg := range map[string]config.Config{
		"checking a local hub": {
			HubRemote: local,
			HubCheck:  config.HubCheck{Enabled: true, TimeoutSeconds: 60},
		},
		"the check turned off": {
			HubRemote: local,
			HubCheck:  config.HubCheck{Enabled: false, TimeoutSeconds: 60},
		},
		"no hub to check": {
			HubCheck: config.HubCheck{Enabled: true, TimeoutSeconds: 60},
		},
		"a hub on another host with no shell account named": {
			HubRemote: "ssh://fleet@elsewhere.example/opt/fleet/poppy-hub.git",
			HubCheck:  config.HubCheck{Enabled: true, TimeoutSeconds: 60},
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := &Agent{Config: cfg, Log: quietLogger()}

			result := a.checkHub(context.Background(), nil)

			if result == nil {
				t.Fatal("the cycle recorded nothing at all; the absence of a check must be a recorded fact")
			}
			if result.CheckedAt.IsZero() {
				t.Error("the attempt has no timestamp")
			}
			if !result.Verdict().Ran() && result.Reason == "" {
				t.Errorf("verdict %s with no reason a person can act on", result.Verdict())
			}
		})
	}
}

func TestTheCheckReachesTheRepositoryTheMachinePublishesTo(t *testing.T) {
	local := scratchHub(t)
	a := &Agent{
		Config: config.Config{
			HubRemote: local,
			HubCheck:  config.HubCheck{Enabled: true, TimeoutSeconds: 60},
		},
		Log: quietLogger(),
	}

	result := a.checkHub(context.Background(), nil)

	if result.Repo != local {
		t.Errorf("checked %q, want the hub remote %q", result.Repo, local)
	}
	if result.Verdict() != hubcheck.StatusClean {
		t.Errorf("verdict = %s, want clean (%s)", result.Verdict(), result.Reason)
	}
}

func TestTheFsckIsDueWheneverThereIsNothingToSayItRanRecently(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	recent := &hubcheck.Result{
		CheckedAt: now.Add(-2 * time.Minute),
		Fsck:      hubcheck.Check{Status: hubcheck.StatusClean},
	}

	for name, c := range map[string]struct {
		prev     *hubcheck.Result
		interval time.Duration
		want     bool
	}{
		"no interval configured":     {recent, 0, true},
		"no previous check":          {nil, time.Hour, true},
		"previous fsck did not run":  {&hubcheck.Result{CheckedAt: now.Add(-time.Minute)}, time.Hour, true},
		"within the interval":        {recent, time.Hour, false},
		"older than the interval":    {&hubcheck.Result{CheckedAt: now.Add(-2 * time.Hour), Fsck: hubcheck.Check{Status: hubcheck.StatusClean}}, time.Hour, true},
		"previous fsck found things": {&hubcheck.Result{CheckedAt: now.Add(-time.Minute), Fsck: hubcheck.Check{Status: hubcheck.StatusFound}}, time.Hour, true},
	} {
		t.Run(name, func(t *testing.T) {
			due, reason := fsckDue(c.prev, c.interval, now)
			if due != c.want {
				t.Errorf("fsckDue = %v, want %v", due, c.want)
			}
			if !due && reason == "" {
				t.Error("the fsck was skipped and nothing says why")
			}
		})
	}
}

func TestASkippedFsckLeavesTheVerdictOffClean(t *testing.T) {
	a := &Agent{
		Config: config.Config{
			HubRemote: scratchHub(t),
			HubCheck:  config.HubCheck{Enabled: true, TimeoutSeconds: 60, FsckIntervalSeconds: 3600},
		},
		Log: quietLogger(),
	}
	prev := &hubcheck.Result{
		CheckedAt: time.Now(),
		Sweep:     hubcheck.Check{Status: hubcheck.StatusClean},
		Fsck:      hubcheck.Check{Status: hubcheck.StatusClean},
	}

	result := a.checkHub(context.Background(), prev)

	if result.Fsck.Status != hubcheck.StatusSkipped {
		t.Fatalf("fsck = %s, want skipped", result.Fsck.Status)
	}
	if result.Verdict() == hubcheck.StatusClean {
		t.Error("the verdict is clean while the fsck did not run this cycle")
	}
}
