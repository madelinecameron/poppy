package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madelinecameron/poppy/internal/hub"
)

// isolate points configuration and state at a temporary directory so a test
// neither reads the developer's own hub nor writes to it.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("POPPY_CONFIG", filepath.Join(dir, "absent.json"))
	t.Setenv("POPPY_DATA_DIR", dir)
	return dir
}

func TestSetupReadCreatesNoHub(t *testing.T) {
	dir := isolate(t)

	_, _, err := setupRead()
	if err == nil {
		t.Fatal("setupRead succeeded with no hub present")
	}
	if !strings.Contains(err.Error(), "poppy scan") {
		t.Errorf("error %q does not say how to record this machine", err)
	}

	// The point of the change: reading must not bring a hub into being. A
	// reader that creates one fails outright on an account that cannot write.
	if _, err := os.Stat(filepath.Join(dir, "hub")); !os.IsNotExist(err) {
		t.Error("setupRead created a hub directory")
	}
}

func TestSetupReadServesAnExistingHub(t *testing.T) {
	dir := isolate(t)
	if _, err := hub.Open(context.Background(), filepath.Join(dir, "hub"), ""); err != nil {
		t.Fatalf("open hub: %v", err)
	}

	_, store, err := setupRead()
	if err != nil {
		t.Fatalf("setupRead: %v", err)
	}
	if _, err := store.Fleet(); err != nil {
		t.Fatalf("fleet: %v", err)
	}
}
