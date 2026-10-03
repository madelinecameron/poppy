package shell

import (
	"os/exec"
	"testing"
)

func TestQuoteSurvivesTheShell(t *testing.T) {
	for _, s := range []string{"", "plain", "with space", "it's", `$HOME "x" \n`, "'"} {
		out, err := exec.Command("sh", "-c", "printf %s "+Quote(s)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != s {
			t.Errorf("Quote(%q) came back as %q", s, out)
		}
	}
}
