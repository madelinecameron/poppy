package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeTool drops a fake fleet binary that answers one version convention, so
// the probe is exercised against both the poppy style (`version`) and the
// houseparty style (`--version`), plus a tool that answers neither.
func writeTool(t *testing.T, dir, name, arg, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX")
	}
	body := "#!/bin/sh\n"
	if arg == "" {
		body += "echo 'usage: " + name + " <command>'\nexit 2\n"
	} else {
		body += "[ \"$1\" = \"" + arg + "\" ] && { echo '" + out + "'; exit 0; }\n" +
			"echo 'unknown command'\nexit 1\n"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestReadContractPullsToolNamesAndRoles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "fabric.yaml")
	os.WriteFile(f, []byte(`
tailnet: example.ts.net
toolchain:
  channel: stable
  tools:
    - name: poppy
      version: v0.2.4
      role: agent
    - name: houseparty
      version: v0.1.8
`), 0o644)

	got := readContract(f)
	if len(got) != 2 {
		t.Fatalf("want 2 tools, got %d", len(got))
	}
	if got[0].Name != "poppy" || got[0].Role != "agent" {
		t.Errorf("first tool = %+v", got[0])
	}
	if got[1].Name != "houseparty" || got[1].Role != "" {
		t.Errorf("second tool = %+v", got[1])
	}
}

// A machine with no contract must still publish something for itself, rather
// than an empty list that reads as "poppy not installed".
func TestReadContractMissingFileIsNotFatal(t *testing.T) {
	if got := readContract(filepath.Join(t.TempDir(), "absent.yaml")); got != nil {
		t.Fatalf("want nil for a missing contract, got %+v", got)
	}
	if got := readContract(""); got != nil {
		t.Fatalf("want nil for an empty path, got %+v", got)
	}
}

func TestProbeVersionHandlesBothConventions(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "poppyish", "version", "poppyish v0.2.4")
	writeTool(t, dir, "housish", "--version", "housish v0.1.8")
	writeTool(t, dir, "silent", "", "")

	for _, tc := range []struct{ name, want string }{
		{"poppyish", "v0.2.4"},
		{"housish", "v0.1.8"},
		{"silent", ""}, // installed, version unknown — not the same as missing
	} {
		if got := probeVersion(context.Background(), filepath.Join(dir, tc.name)); got != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, got)
		}
	}
}

func TestExecutableRejectsMissingAndNonExecutable(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, []byte("x"), 0o644)

	if executable(filepath.Join(dir, "absent")) {
		t.Error("absent file reported executable")
	}
	if executable(plain) {
		t.Error("non-executable file reported executable")
	}
	if executable(dir) {
		t.Error("directory reported executable")
	}
}
