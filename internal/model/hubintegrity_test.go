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
			h := Fleet{Machines: []MachineState{machine("a", hub)}}.HubIntegrity()

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
	}}.HubIntegrity()

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
	}}.HubIntegrity()

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

func TestOneFindingOutranksEveryCleanReport(t *testing.T) {
	corrupt := ran(hubcheck.StatusFound, time.Now())
	corrupt.ZeroCount = 13

	h := Fleet{Machines: []MachineState{
		machine("mac", ran(hubcheck.StatusClean, time.Now())),
		machine("laptop", corrupt),
	}}.HubIntegrity()

	if h.Verdict != hubcheck.StatusFound {
		t.Errorf("verdict = %s, want found", h.Verdict)
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
	}}.HubIntegrity()

	if !h.LastRan.Equal(recent) {
		t.Errorf("lastRan = %s, want %s", h.LastRan, recent)
	}
}

func TestTheLoudestReasonForSilenceIsTheOneReported(t *testing.T) {
	h := Fleet{Machines: []MachineState{
		machine("a", hubcheck.NotRun(hubcheck.StatusSkipped, "not due")),
		machine("b", hubcheck.NotRun(hubcheck.StatusFailed, "unreachable")),
	}}.HubIntegrity()

	if h.Verdict != hubcheck.StatusFailed {
		t.Errorf("verdict = %s, want failed: skipped must not speak for a failure", h.Verdict)
	}
}

func TestAnEmptyFleetSaysNothingRatherThanSayingClean(t *testing.T) {
	h := Fleet{}.HubIntegrity()

	if h.Verdict == hubcheck.StatusClean {
		t.Error("an empty fleet reports a clean hub")
	}
	if len(h.Reports) != 0 {
		t.Errorf("reports = %d, want none", len(h.Reports))
	}
}

func TestAHubNobodyCheckedHasNoLastRan(t *testing.T) {
	data, err := json.Marshal(Fleet{Machines: []MachineState{machine("a", nil)}}.HubIntegrity())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "lastRan") {
		t.Errorf("lastRan present with no check run: %s", data)
	}
}
