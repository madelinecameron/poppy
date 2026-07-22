//go:build darwin

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPlistIsValidAndRunsForeground(t *testing.T) {
	plist := renderPlist("/opt/homebrew/bin/poppy", "/tmp/poppy/daemon.log")

	// The service manager supervises the process, so it must run the loop in
	// the foreground rather than the self-detaching default.
	if !strings.Contains(plist, "<string>--foreground</string>") {
		t.Error("plist does not run the daemon in the foreground")
	}
	if !strings.Contains(plist, serviceLabel) {
		t.Errorf("plist missing label %q", serviceLabel)
	}

	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil unavailable")
	}
	path := filepath.Join(t.TempDir(), "poppy.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil rejected the plist: %v\n%s", err, out)
	}
}

func TestRenderPlistEscapesPaths(t *testing.T) {
	plist := renderPlist("/opt/A&B/poppy", "/tmp/x.log")
	if strings.Contains(plist, "A&B") {
		t.Error("ampersand in path not XML-escaped; plist would be malformed")
	}
	if !strings.Contains(plist, "A&amp;B") {
		t.Error("expected escaped ampersand in path")
	}
}
