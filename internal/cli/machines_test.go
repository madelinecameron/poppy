package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/madelinecameron/poppy/internal/model"
)

func TestCheckRetire(t *testing.T) {
	now := time.Now()
	fleet := model.Fleet{Machines: []model.MachineState{
		{Manifest: model.Manifest{Machine: "Mac.localdomain", Heartbeat: now.Add(-7 * 24 * time.Hour)}},
		{Manifest: model.Manifest{Machine: "backoffice", Heartbeat: now.Add(-time.Minute)}},
		{Manifest: model.Manifest{Machine: "MacBook", Heartbeat: now}},
	}}
	for name, tc := range map[string]struct {
		machine string
		files   int
		force   bool
		wantErr string
	}{
		"a dead machine":           {"Mac.localdomain", 3, false, ""},
		"this machine":             {"macbook", 3, true, "this machine"},
		"a live machine":           {"backoffice", 3, false, "looks alive"},
		"a live machine, forced":   {"backoffice", 3, true, ""},
		"a machine with no record": {"nobody", 0, false, "no records"},
	} {
		err := checkRetire(tc.machine, "MacBook", fleet, tc.files, now, time.Hour, tc.force)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want %q", name, err, tc.wantErr)
		}
	}
}
