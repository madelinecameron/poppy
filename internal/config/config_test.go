package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReturnsDefaultsWhenAbsent(t *testing.T) {
	t.Setenv("POPPY_CONFIG", filepath.Join(t.TempDir(), "missing.json"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Machine == "" {
		t.Error("machine name empty")
	}
	if len(cfg.Roots) == 0 {
		t.Error("no default roots")
	}
	if cfg.ScanIntervalSeconds <= 0 {
		t.Errorf("scan interval = %d", cfg.ScanIntervalSeconds)
	}
}

// A partial configuration is the normal case: users set the few keys they care
// about, and the rest must fill in rather than becoming zero values that would
// disable scanning or spin the daemon.
func TestLoadFillsMissingKeysFromDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"machine":"devbox"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POPPY_CONFIG", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Machine != "devbox" {
		t.Errorf("machine = %q, want devbox", cfg.Machine)
	}
	if cfg.ScanIntervalSeconds != Defaults().ScanIntervalSeconds {
		t.Errorf("interval = %d, want default", cfg.ScanIntervalSeconds)
	}
	if cfg.MaxDepth != Defaults().MaxDepth {
		t.Errorf("maxDepth = %d, want default", cfg.MaxDepth)
	}
	if len(cfg.Roots) == 0 {
		t.Error("roots not defaulted")
	}
}

func TestLoadReportsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POPPY_CONFIG", path)

	if _, err := Load(); err == nil {
		t.Error("invalid configuration accepted")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	t.Setenv("POPPY_CONFIG", path)

	want := Defaults()
	want.Machine = "laptop"
	want.HubRemote = "ssh://devbox/~/poppy-hub.git"
	want.Roots = []string{"~/code", "~/work"}
	if err := Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !Exists() {
		t.Fatal("Exists() = false after save")
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Machine != want.Machine || got.HubRemote != want.HubRemote {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if len(got.Roots) != 2 {
		t.Errorf("roots = %v", got.Roots)
	}
}

func TestDataDirHonoursOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POPPY_DATA_DIR", dir)
	if got := DataDir(); got != dir {
		t.Errorf("DataDir() = %q, want %q", got, dir)
	}
}

func TestHostnameStripsTransientSuffixes(t *testing.T) {
	// macOS reports names such as "studio.local" that change with the network.
	if got := hostname(); got == "" {
		t.Fatal("hostname empty")
	}
	for _, suffix := range []string{".local", ".lan", ".home"} {
		if len(hostname()) > len(suffix) &&
			hostname()[len(hostname())-len(suffix):] == suffix {
			t.Errorf("hostname %q retains transient suffix %q", hostname(), suffix)
		}
	}
}
