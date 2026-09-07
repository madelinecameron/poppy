package config

import (
	"strings"
	"testing"
)

func TestHubCheckTargetTakesTheRepositoryFromTheHubRemote(t *testing.T) {
	// The account in the hub remote runs git-shell and can answer nothing but
	// git commands, so the shell account comes from the mirror, which is the
	// same host and one poppy already reaches every cycle.
	cfg := Config{
		HubRemote: "ssh://fleet@backoffice.example.ts.net/opt/fleet/poppy-hub.git",
		Snapshot: Snapshot{
			Mirror: Mirror{Enabled: true, Host: "mads@backoffice.example.ts.net", Dir: "/opt/fleet/poppy-mirrors"},
		},
	}

	opts, reason := cfg.HubCheckTarget()

	if reason != "" {
		t.Fatalf("unexpected reason %q", reason)
	}
	if opts.Repo != "/opt/fleet/poppy-hub.git" {
		t.Errorf("repo = %q", opts.Repo)
	}
	if opts.Host != "mads@backoffice.example.ts.net" {
		t.Errorf("host = %q, want the mirror's shell account", opts.Host)
	}
}

func TestAHubOnAnotherHostWithNoShellAccountSaysSo(t *testing.T) {
	cfg := Config{HubRemote: "ssh://fleet@backoffice.example.ts.net/opt/fleet/poppy-hub.git"}

	_, reason := cfg.HubCheckTarget()

	if reason == "" {
		t.Fatal("no reason given; the check would go quiet with nothing to explain it")
	}
	if !strings.Contains(reason, "hubCheck.host") {
		t.Errorf("reason %q does not name the setting to change", reason)
	}
}

func TestALocalOnlyMachineSaysItHasNoHubToCheck(t *testing.T) {
	_, reason := Config{}.HubCheckTarget()

	if reason == "" {
		t.Fatal("a machine with no hub reports nothing about why it checked nothing")
	}
}

func TestAHubOnThisMachineIsCheckedWithoutSSH(t *testing.T) {
	cfg := Config{HubRemote: "/opt/fleet/poppy-hub.git"}

	opts, reason := cfg.HubCheckTarget()

	if reason != "" {
		t.Fatalf("unexpected reason %q", reason)
	}
	if opts.Host != "" {
		t.Errorf("host = %q, want the check to run here", opts.Host)
	}
	if opts.Repo != "/opt/fleet/poppy-hub.git" {
		t.Errorf("repo = %q", opts.Repo)
	}
}

func TestExplicitSettingsWinOverWhatIsDerived(t *testing.T) {
	cfg := Config{
		HubRemote: "ssh://fleet@backoffice.example.ts.net/opt/fleet/poppy-hub.git",
		HubCheck:  HubCheck{Host: "ops@backoffice", Repo: "/srv/other.git"},
	}

	opts, reason := cfg.HubCheckTarget()

	if reason != "" {
		t.Fatalf("unexpected reason %q", reason)
	}
	if opts.Repo != "/srv/other.git" || opts.Host != "ops@backoffice" {
		t.Errorf("opts = %+v, want the configured overrides", opts)
	}
}

func TestTheCheckIsOnByDefault(t *testing.T) {
	if !Defaults().HubCheck.Enabled {
		t.Error("a fleet that has to opt in to noticing corruption will not notice it")
	}
	if Defaults().HubCheck.FsckIntervalSeconds != 0 {
		t.Error("the default fsck cadence is not poppy's own scan cadence")
	}
}
