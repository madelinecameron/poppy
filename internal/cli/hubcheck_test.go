package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/hubcheck"
	"github.com/madelinecameron/poppy/internal/model"
)

func fleetWith(hub *hubcheck.Result) model.Fleet {
	return model.Fleet{Machines: []model.MachineState{
		{Manifest: model.Manifest{Machine: "mac", Hub: hub}},
	}}
}

func renderIntegrity(f model.Fleet) string {
	var b bytes.Buffer
	printHubIntegrity(&b, f.HubIntegrity(), time.Now())
	return b.String()
}

// The requirement, at the surface a person actually reads: a hub that was
// checked and a hub that was not must not produce the same pane.
func TestThePaneDistinguishesCheckedFromUnchecked(t *testing.T) {
	checked := &hubcheck.Result{
		CheckedAt: time.Now(),
		Repo:      "/opt/fleet/poppy-hub.git",
		Sweep:     hubcheck.Check{Status: hubcheck.StatusClean},
		Fsck:      hubcheck.Check{Status: hubcheck.StatusClean},
	}
	unchecked := hubcheck.NotRun(hubcheck.StatusFailed, "ssh: connection refused")

	clean := renderIntegrity(fleetWith(checked))
	silent := renderIntegrity(fleetWith(unchecked))

	if clean == silent {
		t.Fatal("a checked hub and an unchecked one render identically")
	}
	if !strings.Contains(clean, "clean") {
		t.Errorf("a clean check does not say so:\n%s", clean)
	}
	if strings.Contains(silent, "clean") {
		t.Errorf("an unchecked hub renders the word clean:\n%s", silent)
	}
	if !strings.Contains(silent, "NOT CHECKED") {
		t.Errorf("an unchecked hub does not say it was not checked:\n%s", silent)
	}
	if !strings.Contains(silent, "connection refused") {
		t.Errorf("an unchecked hub does not say why:\n%s", silent)
	}
}

// A machine going quiet must survive a fleet where another machine reported
// cleanly. This is the shape that hides a problem: one good answer looks like
// the answer.
func TestACleanVerdictStillNamesTheMachineThatCouldNotLook(t *testing.T) {
	out := renderIntegrity(model.Fleet{Machines: []model.MachineState{
		{Manifest: model.Manifest{Machine: "mac", Hub: &hubcheck.Result{
			CheckedAt: time.Now(),
			Sweep:     hubcheck.Check{Status: hubcheck.StatusClean},
			Fsck:      hubcheck.Check{Status: hubcheck.StatusClean},
		}}},
		{Manifest: model.Manifest{Machine: "laptop",
			Hub: hubcheck.NotRun(hubcheck.StatusFailed, "ssh: no route to host")}},
	}})

	for _, want := range []string{"clean", "1 machine could not check", "laptop", "no route to host"} {
		if !strings.Contains(out, want) {
			t.Errorf("the pane omits %q:\n%s", want, out)
		}
	}
}

func TestAMachineWhosePoppyRecordsNoCheckIsNamedNotSkipped(t *testing.T) {
	out := renderIntegrity(fleetWith(nil))

	if !strings.Contains(out, "mac") {
		t.Errorf("the machine is not named:\n%s", out)
	}
	if !strings.Contains(out, "NOT CHECKED") {
		t.Errorf("a missing check does not read as a missing check:\n%s", out)
	}
}

func TestAMachineWithNoHubToCheckIsNotDraggedIntoThePane(t *testing.T) {
	out := renderIntegrity(fleetWith(hubcheck.NotRun(hubcheck.StatusNotConfigured, "no hub configured")))

	if out != "" {
		t.Errorf("a local-only machine is reported as a hub problem:\n%s", out)
	}
}

func TestTheFindingNamesTheFilesItFound(t *testing.T) {
	found := &hubcheck.Result{
		CheckedAt: time.Now(),
		Reason:    "13 zero-length file(s) in the object store",
		Sweep: hubcheck.Check{
			Status:    hubcheck.StatusFound,
			Truncated: true,
		},
		Fsck:      hubcheck.Check{Status: hubcheck.StatusClean},
		Zero:      []string{"/opt/fleet/poppy-hub.git/objects/09/339d30"},
		ZeroCount: 13,
	}

	out := renderIntegrity(fleetWith(found))

	for _, want := range []string{"CORRUPT", "zero-length", "objects/09/339d30", "12 more not listed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the pane omits %q:\n%s", want, out)
		}
	}
}

func TestTheHandRunReportShowsEachCheckSeparately(t *testing.T) {
	var b bytes.Buffer
	printHubResult(&b, &hubcheck.Result{
		Repo:      "/opt/fleet/poppy-hub.git",
		Host:      "mads@backoffice",
		CheckedAt: time.Now(),
		Sweep:     hubcheck.Check{Status: hubcheck.StatusClean},
		Fsck:      hubcheck.Check{Status: hubcheck.StatusSkipped, Detail: []string{"--no-fsck was passed"}},
	})
	out := b.String()

	if !strings.Contains(out, "sweep") || !strings.Contains(out, "fsck") {
		t.Errorf("the report does not name both checks:\n%s", out)
	}
	if !strings.Contains(out, "--no-fsck was passed") {
		t.Errorf("the skipped check does not say why it was skipped:\n%s", out)
	}
	// The headline must not read clean while half of it was skipped.
	if strings.HasPrefix(strings.TrimSpace(out), "HUB  clean") {
		t.Errorf("the headline is clean while the fsck was skipped:\n%s", out)
	}
}
