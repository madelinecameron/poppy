package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madelinecameron/poppy/internal/hub"
	"github.com/madelinecameron/poppy/internal/model"
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

	_, _, err := setupRead(context.Background())
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

	_, store, err := setupRead(context.Background())
	if err != nil {
		t.Fatalf("setupRead: %v", err)
	}
	if _, err := store.Fleet(); err != nil {
		t.Fatalf("fleet: %v", err)
	}
}

func TestHubDirReadsAnotherPoppysCopyAndRefusesToPublish(t *testing.T) {
	dir := isolate(t)
	owner := filepath.Join(t.TempDir(), "hub")
	ownerStore, err := hub.Open(context.Background(), owner, "")
	if err != nil {
		t.Fatalf("open owner hub: %v", err)
	}
	if _, err := ownerStore.Publish(context.Background(), model.Manifest{Machine: "core"}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	t.Setenv("POPPY_HUB_DIR", owner)

	_, store, err := setupRead(context.Background())
	if err != nil {
		t.Fatalf("setupRead: %v", err)
	}
	fleet, err := store.Fleet()
	if err != nil {
		t.Fatalf("fleet: %v", err)
	}
	if len(fleet.Machines) != 1 {
		t.Errorf("machines = %d, want the owner's one", len(fleet.Machines))
	}
	if _, _, err := setup(context.Background()); err == nil || !strings.Contains(err.Error(), owner) {
		t.Errorf("setup err = %v, want a refusal naming %s", err, owner)
	}
	if _, err := os.Stat(filepath.Join(dir, "hub")); !os.IsNotExist(err) {
		t.Error("a reader of another poppy's copy created a hub of its own")
	}
}
