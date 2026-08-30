package cli

import (
	"testing"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/workplace-protocols/discovery"
)

func TestApplyWorkplaceProtocolsFillsHubAndMirror(t *testing.T) {
	cfg := config.Defaults()
	m := &discovery.Manifest{
		Hub:    discovery.Hub{Remote: "ssh://devbox/~/poppy-hub.git"},
		Mirror: discovery.Mirror{Host: "devbox.mads.ts.net", Dir: "/home/mads/poppy-mirrors"},
	}
	applyWorkplaceProtocols(&cfg, m)

	if cfg.HubRemote != "ssh://devbox/~/poppy-hub.git" {
		t.Errorf("hub = %q", cfg.HubRemote)
	}
	if !cfg.Snapshot.Mirror.Enabled || !cfg.Snapshot.Enabled {
		t.Error("mirror + snapshot should be enabled when the manifest declares a mirror")
	}
	if cfg.Snapshot.Mirror.Host != "devbox.mads.ts.net" || cfg.Snapshot.Mirror.Dir != "/home/mads/poppy-mirrors" {
		t.Errorf("mirror = %+v", cfg.Snapshot.Mirror)
	}
}

func TestApplyWorkplaceProtocolsExplicitHubWins(t *testing.T) {
	cfg := config.Defaults()
	cfg.HubRemote = "ssh://explicit/hub.git" // e.g. from --hub
	m := &discovery.Manifest{Hub: discovery.Hub{Remote: "ssh://devbox/hub.git"}}
	applyWorkplaceProtocols(&cfg, m)
	if cfg.HubRemote != "ssh://explicit/hub.git" {
		t.Errorf("explicit --hub should win, got %q", cfg.HubRemote)
	}
}

func TestApplyWorkplaceProtocolsNoMirrorLeavesItOff(t *testing.T) {
	cfg := config.Defaults()
	m := &discovery.Manifest{Hub: discovery.Hub{Remote: "ssh://devbox/hub.git"}} // no mirror
	applyWorkplaceProtocols(&cfg, m)
	if cfg.Snapshot.Mirror.Enabled {
		t.Error("a manifest without a mirror must not enable mirroring")
	}
}
