package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/hubcheck"
)

func machine(name string, hub *hubcheck.Result) MachineState {
	return MachineState{Manifest: Manifest{Machine: name, Hub: hub}}
}

func ran(status hubcheck.Status, at time.Time) *hubcheck.Result {
	return &hubcheck.Result{
		CheckedAt: at,
		Sweep:     hubcheck.Check{Status: status},
		Fsck:      hubcheck.Check{Status: status},
	}
}

func TestAFleetWhereNobodyLookedIsNotClean(t *testing.T) {
	for name, hub := range map[string]*hubcheck.Result{
		"no check recorded at all": nil,
		"the host was unreachable": hubcheck.NotRun(hubcheck.StatusFailed, "ssh refused"),
		"the check was disabled":   hubcheck.NotRun(hubcheck.StatusSkipped, "hubCheck.enabled is false"),
	} {
		t.Run(name, func(t *testing.T) {
			h := Fleet{Machines: []MachineState{machine("a", hub)}}.HubIntegrity(time.Now(), 0)

			if h.Verdict == hubcheck.StatusClean {
				t.Error("verdict = clean when nothing examined the hub")
			}
			if h.Ran != 0 {
				t.Errorf("ran = %d, want 0", h.Ran)
			}
			if h.Silent != 1 {
				t.Errorf("silent = %d, want 1: a machine that did not check must be counted", h.Silent)
			}
			if len(h.Reports) != 1 {
				t.Errorf("reports = %d, want the silent machine named", len(h.Reports))
			}
		})
	}
}

func TestAMachineWithNoHubToCheckIsNotCountedAsSilent(t *testing.T) {
	// The workplace box runs a local-only poppy: it publishes to no shared
	// hub, so it is not failing to check one.
	h := Fleet{Machines: []MachineState{
		machine("workstation", ran(hubcheck.StatusClean, time.Now())),
		machine("local-only", hubcheck.NotRun(hubcheck.StatusNotConfigured, "no hub configured")),
	}}.HubIntegrity(time.Now(), 0)

	if h.Silent != 0 {
		t.Errorf("silent = %d, want 0", h.Silent)
	}
	if h.Ran != 1 {
		t.Errorf("ran = %d, want 1", h.Ran)
	}
	if h.Verdict != hubcheck.StatusClean {
		t.Errorf("verdict = %s, want clean", h.Verdict)
	}
	if len(h.Reports) != 1 {
		t.Errorf("reports = %d, want only the machine with a hub to check", len(h.Reports))
	}
}

// One machine checking successfully does not excuse another that could not.
// The verdict is what the successful check found; the count of machines that
// went quiet is reported beside it and never absorbed into it.
func TestASuccessfulCheckDoesNotHideAFailedOne(t *testing.T) {
	h := Fleet{Machines: []MachineState{
		machine("mac", ran(hubcheck.StatusClean, time.Now())),
		machine("laptop", hubcheck.NotRun(hubcheck.StatusFailed, "ssh: connection refused")),
	}}.HubIntegrity(time.Now(), 0)

	if h.Verdict != hubcheck.StatusClean {
		t.Errorf("verdict = %s, want clean: a check did run and found nothing", h.Verdict)
	}
	if h.Silent != 1 {
		t.Fatalf("silent = %d, want 1", h.Silent)
	}
	if h.Ran != 1 {
		t.Errorf("ran = %d, want 1", h.Ran)
	}
}

func TestOneFindingOutranksEveryCleanReportOfAnotherRepository(t *testing.T) {
	now := time.Now()
	corrupt := ran(hubcheck.StatusFound, now)
	corrupt.Repo = "/opt/fleet/poppy-hub.git"
	corrupt.ZeroCount = 13
	clean := ran(hubcheck.StatusClean, now)
	clean.Repo = "/opt/fleet/other.git"

	h := Fleet{Machines: []MachineState{
		machine("mac", clean),
		machine("laptop", corrupt),
	}}.HubIntegrity(now, 0)

	if h.Verdict != hubcheck.StatusFound {
		t.Errorf("verdict = %s, want found", h.Verdict)
	}
}

const hubRepo = "/opt/fleet/poppy-hub.git"

func live(name string, now time.Time, status hubcheck.Status, checked time.Time) MachineState {
	r := ran(status, checked)
	r.Host, r.Repo = "fleet@backoffice", hubRepo
	return MachineState{Manifest: Manifest{Machine: name, Heartbeat: now, Hub: r}}
}

// The newest look at a repository says what is in it now. A repair after a
// finding must be able to turn the verdict clean again.
func TestTheNewestCheckOfARepositoryDecidesItsVerdict(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	h := Fleet{Machines: []MachineState{
		live("laptop", now, hubcheck.StatusFound, now.Add(-20*time.Minute)),
		live("mac", now, hubcheck.StatusClean, now.Add(-5*time.Minute)),
	}}.HubIntegrity(now, time.Hour)

	if h.Verdict != hubcheck.StatusClean {
		t.Errorf("verdict = %s, want clean: a newer check found nothing", h.Verdict)
	}
	if h.Ran != 2 || h.Silent != 0 {
		t.Errorf("ran = %d, silent = %d, want 2 and 0", h.Ran, h.Silent)
	}
	if !h.Reports[0].Superseded || h.Reports[1].Superseded {
		t.Errorf("superseded = %v, %v, want the older check marked", h.Reports[0].Superseded, h.Reports[1].Superseded)
	}

	h = Fleet{Machines: []MachineState{
		live("mac", now, hubcheck.StatusClean, now.Add(-20*time.Minute)),
		live("laptop", now, hubcheck.StatusFound, now.Add(-5*time.Minute)),
	}}.HubIntegrity(now, time.Hour)

	if h.Verdict != hubcheck.StatusFound {
		t.Errorf("verdict = %s, want found: the newest check found corruption", h.Verdict)
	}
}

// The case that kept the fleet red: a machine that stopped publishing a week
// ago left "found" on its last document, while the machines still running
// check the same repository cleanly.
func TestAMachineThatStoppedReportingCannotHoldTheVerdict(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	gone := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

	h := Fleet{Machines: []MachineState{
		live("Mac.localdomain", gone, hubcheck.StatusFound, gone),
		live("backoffice", now, hubcheck.StatusClean, now.Add(-3*time.Minute)),
		live("MacBook", now, hubcheck.StatusClean, now.Add(-7*time.Minute)),
	}}.HubIntegrity(now, time.Hour)

	if h.Verdict != hubcheck.StatusClean {
		t.Errorf("verdict = %s, want clean", h.Verdict)
	}
	if h.Ran != 2 {
		t.Errorf("ran = %d, want 2", h.Ran)
	}
	if h.Silent != 1 {
		t.Errorf("silent = %d, want 1: the stale machine is not checking", h.Silent)
	}
	if !h.Reports[0].Stale {
		t.Error("the stale machine's report is not marked stale")
	}
}

// A live heartbeat does not make an old check current.
func TestAnOldCheckFromALiveMachineIsSilent(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	h := Fleet{Machines: []MachineState{
		live("mac", now, hubcheck.StatusFound, now.Add(-3*time.Hour)),
	}}.HubIntegrity(now, time.Hour)

	if h.Verdict == hubcheck.StatusFound || h.Verdict == hubcheck.StatusClean {
		t.Errorf("verdict = %s, want neither found nor clean when no current check ran", h.Verdict)
	}
	if h.Ran != 0 || h.Silent != 1 {
		t.Errorf("ran = %d, silent = %d, want 0 and 1", h.Ran, h.Silent)
	}
}

func TestTheFleetReportsTheMostRecentCheckThatActuallyRan(t *testing.T) {
	old := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	// A failed attempt is timestamped too, and its timestamp must not be
	// mistaken for a look at the repository.
	failed := hubcheck.NotRun(hubcheck.StatusFailed, "unreachable")
	failed.CheckedAt = recent.Add(time.Hour)

	h := Fleet{Machines: []MachineState{
		machine("a", ran(hubcheck.StatusClean, old)),
		machine("b", ran(hubcheck.StatusClean, recent)),
		machine("c", failed),
	}}.HubIntegrity(time.Now(), 0)

	if !h.LastRan.Equal(recent) {
		t.Errorf("lastRan = %s, want %s", h.LastRan, recent)
	}
}

func TestTheLoudestReasonForSilenceIsTheOneReported(t *testing.T) {
	h := Fleet{Machines: []MachineState{
		machine("a", hubcheck.NotRun(hubcheck.StatusSkipped, "not due")),
		machine("b", hubcheck.NotRun(hubcheck.StatusFailed, "unreachable")),
	}}.HubIntegrity(time.Now(), 0)

	if h.Verdict != hubcheck.StatusFailed {
		t.Errorf("verdict = %s, want failed: skipped must not speak for a failure", h.Verdict)
	}
}

func TestAnEmptyFleetSaysNothingRatherThanSayingClean(t *testing.T) {
	h := Fleet{}.HubIntegrity(time.Now(), 0)

	if h.Verdict == hubcheck.StatusClean {
		t.Error("an empty fleet reports a clean hub")
	}
	if len(h.Reports) != 0 {
		t.Errorf("reports = %d, want none", len(h.Reports))
	}
}

func TestAHubNobodyCheckedHasNoLastRan(t *testing.T) {
	data, err := json.Marshal(Fleet{Machines: []MachineState{machine("a", nil)}}.HubIntegrity(time.Now(), 0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "lastRan") {
		t.Errorf("lastRan present with no check run: %s", data)
	}
}
