package cli

import (
	"testing"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/workplace-protocols/discovery"
)

func manifest(services ...discovery.Service) *discovery.Manifest {
	return &discovery.Manifest{Tailnet: "example.ts.net", Services: services}
}

func TestApplyWorkplaceProtocolsFillsHubAndMirror(t *testing.T) {
	cfg := config.Defaults()
	m := manifest(
		discovery.Service{Name: "hub", URL: "ssh://devbox/~/poppy-hub.git"},
		discovery.Service{Name: "mirror", URL: "ssh://mads@devbox.mads.ts.net/home/mads/poppy-mirrors"},
	)
	if err := applyWorkplaceProtocols(&cfg, m); err != nil {
		t.Fatal(err)
	}

	if cfg.HubRemote != "ssh://devbox/~/poppy-hub.git" {
		t.Errorf("hub = %q", cfg.HubRemote)
	}
	if !cfg.Snapshot.Mirror.Enabled || !cfg.Snapshot.Enabled {
		t.Error("mirror + snapshot should be enabled when the manifest declares a mirror")
	}
	if cfg.Snapshot.Mirror.Host != "mads@devbox.mads.ts.net" || cfg.Snapshot.Mirror.Dir != "/home/mads/poppy-mirrors" {
		t.Errorf("mirror = %+v", cfg.Snapshot.Mirror)
	}
}

func TestApplyWorkplaceProtocolsExplicitHubWins(t *testing.T) {
	cfg := config.Defaults()
	cfg.HubRemote = "ssh://explicit/hub.git" // e.g. from --hub
	if err := applyWorkplaceProtocols(&cfg, manifest(discovery.Service{Name: "hub", URL: "ssh://devbox/hub.git"})); err != nil {
		t.Fatal(err)
	}
	if cfg.HubRemote != "ssh://explicit/hub.git" {
		t.Errorf("explicit --hub should win, got %q", cfg.HubRemote)
	}
}

func TestApplyWorkplaceProtocolsNoMirrorLeavesItOff(t *testing.T) {
	cfg := config.Defaults()
	if err := applyWorkplaceProtocols(&cfg, manifest(discovery.Service{Name: "hub", URL: "ssh://devbox/hub.git"})); err != nil {
		t.Fatal(err)
	}
	if cfg.Snapshot.Mirror.Enabled {
		t.Error("a manifest without a mirror must not enable mirroring")
	}
}

func TestMirrorTarget(t *testing.T) {
	cases := map[string][2]string{
		"ssh://mads@box.ts.net/opt/mirrors": {"mads@box.ts.net", "/opt/mirrors"},
		"ssh://box/~/mirrors":               {"box", "~/mirrors"},
		"file:///srv/mirrors":               {"", "/srv/mirrors"},
	}
	for raw, want := range cases {
		host, dir, err := mirrorTarget(raw)
		if err != nil || host != want[0] || dir != want[1] {
			t.Errorf("mirrorTarget(%q) = %q, %q, %v; want %q, %q", raw, host, dir, err, want[0], want[1])
		}
	}
	for _, raw := range []string{"https://box/mirrors", "ssh://box", "ssh:///dir"} {
		if _, _, err := mirrorTarget(raw); err == nil {
			t.Errorf("mirrorTarget(%q) should fail", raw)
		}
	}
}
