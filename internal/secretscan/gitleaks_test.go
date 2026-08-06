package secretscan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/madelinecameron/poppy/internal/secrets"
)

// gitleaks writes the credential itself into its report, under Secret and
// Match. Poppy reads such a report and must come away holding neither.
const gitleaksReport = `[
  {
    "RuleID": "aws-access-token",
    "Description": "AWS Access Token",
    "StartLine": 12,
    "EndLine": 12,
    "File": "/home/mads/code/api/deploy/config.tf",
    "Secret": "AKIAQWERTYUIOPASDFGH",
    "Match": "aws_access_key_id = AKIAQWERTYUIOPASDFGH",
    "Entropy": 3.4
  },
  {
    "RuleID": "Generic API Key",
    "StartLine": 3,
    "File": "/home/mads/code/api/app.py",
    "Secret": "s3cret-value-here",
    "Match": "key = s3cret-value-here"
  }
]`

func TestParseGitleaksKeepsLocationsOnly(t *testing.T) {
	hits := parseGitleaks([]byte(gitleaksReport), "/home/mads/code/api")
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}

	if got := hits[0].loc.Path; got != "deploy/config.tf" {
		t.Errorf("path = %q, want it relative to the repository", got)
	}
	if hits[0].hit.Line != 12 {
		t.Errorf("line = %d, want 12", hits[0].hit.Line)
	}
	if hits[0].loc.Origin != secrets.OriginWorktree {
		t.Errorf("origin = %q, want %q", hits[0].loc.Origin, secrets.OriginWorktree)
	}

	findings := secrets.Findings(hits[0].loc, []secrets.Hit{hits[0].hit})
	data, err := json.Marshal(findings)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"AKIAQWERTYUIOPASDFGH", "aws_access_key_id ="} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("a value from the gitleaks report survived into a finding: %s", data)
		}
	}
}

func TestGitleaksRuleIDsAreSanitized(t *testing.T) {
	tests := map[string]secrets.RuleID{
		"aws-access-token":       "gitleaks:aws-access-token",
		"Generic API Key":        "gitleaks:generic-api-key",
		"weird\nrule\x00id":      "gitleaks:weirdruleid",
		"":                       "gitleaks:unnamed",
		strings.Repeat("x", 200): secrets.RuleID("gitleaks:" + strings.Repeat("x", 48)),
	}
	for in, want := range tests {
		if got := gitleaksRule(in); got != want {
			t.Errorf("gitleaksRule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseGitleaksToleratesRubbish(t *testing.T) {
	if hits := parseGitleaks([]byte("not json"), "/tmp"); hits != nil {
		t.Errorf("got %v, want nil", hits)
	}
}
