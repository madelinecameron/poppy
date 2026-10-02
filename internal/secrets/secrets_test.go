package secrets

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Credential-shaped strings are assembled at run time rather than written as
// literals, so this file does not become a finding every time poppy scans its
// own repository.
func ghToken() string      { return "ghp_" + strings.Repeat("A1b2C3d4", 4) + "wxyz" }
func awsKeyID() string     { return "AKIA" + "QWERTYUIOPASDFGH" }
func anthropicKey() string { return "sk-" + "ant-api03-" + strings.Repeat("Xy7z", 12) }
func openAIKey() string    { return "sk-" + strings.Repeat("Kq3mZp8v", 6) }
func slackToken() string   { return "xoxb-" + "2183746501-9182736450-" + strings.Repeat("Ab3d", 6) }

// TestFindingCannotCarryTheMatchedValue is the structural half of the
// guarantee poppy's state documents rest on. The prose half — that matching
// happens against bytes that never leave this package — is only credible
// because there is nowhere for those bytes to go.
func TestFindingCannotCarryTheMatchedValue(t *testing.T) {
	for _, f := range reflect.VisibleFields(reflect.TypeFor[Finding]()) {
		if f.IsExported() {
			t.Errorf("Finding.%s is exported, so a caller could assign a matched value to it", f.Name)
		}
	}

	// Hit is the only thing that crosses from matching to recording. If it
	// ever grows a field that could hold content, the guarantee is gone.
	want := []string{"Rule", "Line"}
	var got []string
	for _, f := range reflect.VisibleFields(reflect.TypeFor[Hit]()) {
		got = append(got, f.Name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("Hit fields = %v, want %v", got, want)
	}
}

func TestFindingJSONIsLocationOnly(t *testing.T) {
	f := Findings(Location{Origin: OriginStash, Ref: "stash@{1}", Path: "a/b.go"},
		[]Hit{{Rule: RuleGitHubToken, Line: 7}})[0]

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	want := []string{"line", "origin", "path", "ref", "rule"}
	var got []string
	for k := range keys {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("JSON keys = %v, want %v", got, want)
	}
}

// TestUnmarshalDropsSmuggledValues covers the direction the type system cannot:
// a document arriving from the hub, written by something that did not honour
// the rule.
func TestUnmarshalDropsSmuggledValues(t *testing.T) {
	raw := `{"rule":"github-token","origin":"stash","ref":"stash@{0}","path":"a.go","line":3,"value":"` + ghToken() + `","match":"leaked"}`

	var f Finding
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ghToken()) || strings.Contains(string(out), "leaked") {
		t.Fatalf("a smuggled value survived a round trip: %s", out)
	}
	if f.Rule() != RuleGitHubToken || f.Line() != 3 || f.Path() != "a.go" {
		t.Errorf("known fields were lost: %+v", f)
	}
}

func TestScanReportsRuleAndLine(t *testing.T) {
	content := "package main\n\nconst token = \"" + ghToken() + "\"\n"

	hits := Scan("main.go", []byte(content))
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %v", len(hits), hits)
	}
	if hits[0].Rule != RuleGitHubToken {
		t.Errorf("rule = %q, want %q", hits[0].Rule, RuleGitHubToken)
	}
	if hits[0].Line != 3 {
		t.Errorf("line = %d, want 3", hits[0].Line)
	}
}

func TestRules(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		want RuleID // empty means the line must not be reported
	}{
		{"private key block", "id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----", RulePrivateKey},
		{"rsa private key", "key.pem", "-----BEGIN RSA PRIVATE KEY-----", RulePrivateKey},
		{"public key block", "id_rsa.pub", "-----BEGIN PUBLIC KEY-----", ""},
		{"aws access key id", "deploy.sh", "export AWS_ACCESS_KEY_ID=" + awsKeyID(), RuleAWSAccessKeyID},
		{"aws documentation key", "README.md", "AKIAIOSFODNN7EXAMPLE", ""},
		{"github token", "main.go", "client.Auth(\"" + ghToken() + "\")", RuleGitHubToken},
		{"github token prefix alone", "main.go", "const prefix = \"ghp_\"", ""},
		{"anthropic key", "config.py", "ANTHROPIC = '" + anthropicKey() + "'", RuleAnthropicKey},
		{"provider key", "config.py", "openai.api_key = '" + openAIKey() + "'", RuleProviderKey},
		{"slack bot token", "app.js", "const t = '" + slackToken() + "'", RuleSlackToken},
		{"slack webhook", "notify.rb", "https://hooks.slack.com/services/T01ABCDEF/B02GHIJKL/" + strings.Repeat("Zq4m", 6), RuleSlackWebhook},
		{"stripe live key", "billing.go", "stripe.Key = \"sk_live_" + strings.Repeat("4RtY", 6) + "\"", RuleStripeSecretKey},
		{"stripe test key", "billing_test.go", "stripe.Key = \"sk_test_" + strings.Repeat("4RtY", 6) + "\"", ""},

		{"env password", ".env", "DATABASE_PASSWORD=pXq7-Zv2Kd91mLw", RuleEnvAssignment},
		{"env exported token", ".env.production", "export API_TOKEN=Qw8vZ2mK4pLr9dTx", RuleEnvAssignment},
		{"env placeholder", ".env.example", "API_TOKEN=your-token-here", ""},
		{"env changeme", ".env.example", "DB_PASSWORD=changeme-please", ""},
		{"env interpolation", ".env", "API_TOKEN=${VAULT_API_TOKEN}", ""},
		{"env short value", ".env", "API_KEY=1234", ""},
		{"env boolean", ".env", "USE_SECRET=true", ""},
		{"env public key", ".env", "STRIPE_PUBLIC_KEY=pk-9dJk2LmQ8vTx4Rn", ""},
		{"env low entropy", ".env", "API_PASSWORD=aaaaaaaaaaaaaaaa", ""},
		{"assignment outside dotenv", "settings.py", "API_TOKEN = 'Qw8vZ2mK4pLr9dTx'", ""},
		{"non-secret name in dotenv", ".env", "DATABASE_URL=postgres://u:p@localhost/db", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := Scan(tt.path, []byte(tt.body+"\n"))
			switch {
			case tt.want == "" && len(hits) > 0:
				t.Fatalf("false positive: %v", hits)
			case tt.want == "":
				return
			case len(hits) == 0:
				t.Fatalf("no hit, want %q", tt.want)
			}
			if hits[0].Rule != tt.want {
				t.Errorf("rule = %q, want %q", hits[0].Rule, tt.want)
			}
		})
	}
}

// TestAnthropicKeyIsReportedOnce guards the overlap between the vendor rule and
// the generic sk- rule: one credential should not become two findings.
func TestAnthropicKeyIsReportedOnce(t *testing.T) {
	hits := Scan("app.py", []byte("key = '"+anthropicKey()+"'\n"))
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %v", len(hits), hits)
	}
	if hits[0].Rule != RuleAnthropicKey {
		t.Errorf("rule = %q, want %q", hits[0].Rule, RuleAnthropicKey)
	}
}

func TestBinaryContentIsNotScanned(t *testing.T) {
	content := append([]byte("\x00\x01\x02"), []byte(ghToken())...)
	if hits := Scan("blob.bin", content); len(hits) != 0 {
		t.Errorf("scanned binary content: %v", hits)
	}
}

func TestHitsPerFileAreBounded(t *testing.T) {
	line := "-----BEGIN PRIVATE KEY-----\n"
	hits := Scan("key.pem", []byte(strings.Repeat(line, MaxHitsPerFile*3)))
	if len(hits) != MaxHitsPerFile {
		t.Errorf("got %d hits, want the cap of %d", len(hits), MaxHitsPerFile)
	}
}

func TestLongLinesAreTruncated(t *testing.T) {
	padded := strings.Repeat("a", MaxLineBytes) + ghToken()
	if hits := Scan("bundle.min.js", []byte(padded)); len(hits) != 0 {
		t.Errorf("scanned past the line limit: %v", hits)
	}
}

// TestDotenvTemplatesAreNotAssignmentSources is the rule that keeps the
// scanner quiet enough to be left switched on: a committed template holding a
// generated development value is the commonest false positive there is.
func TestDotenvTemplatesAreNotAssignmentSources(t *testing.T) {
	line := "SESSION_SECRET=Qw8vZ2mK4pLr9dTxA3nB7cVf1gH5jK2m"

	for _, p := range []string{".env.example", ".env.sample", ".env.template", "api/.env.dist", ".env.example.local"} {
		if hits := Scan(p, []byte(line+"\n")); len(hits) != 0 {
			t.Errorf("%s: got %v, want the template to be quiet", p, hits)
		}
	}
	for _, p := range []string{".env", ".env.production", "deploy/staging.env", ".envrc"} {
		if hits := Scan(p, []byte(line+"\n")); len(hits) == 0 {
			t.Errorf("%s: no hit, want the assignment reported", p)
		}
	}
	// Naming a file a template excuses a placeholder, never a real credential.
	if hits := Scan(".env.example", []byte("GITHUB_TOKEN="+ghToken()+"\n")); len(hits) != 1 {
		t.Errorf("a real token in a template was missed: %v", hits)
	}
}

func TestReportNoteMergesReasons(t *testing.T) {
	var r Report
	r.Note(SkipBinary, 1, 0)
	r.Note(SkipBinary, 2, 0)
	r.Note(SkipBlobs, 5, 100)

	if len(r.Skipped) != 2 {
		t.Fatalf("got %d skips, want 2: %v", len(r.Skipped), r.Skipped)
	}
	if r.Skipped[0].Count != 3 {
		t.Errorf("binary skips = %d, want 3", r.Skipped[0].Count)
	}
	if r.Skipped[1].Limit != 100 {
		t.Errorf("blob limit = %d, want 100", r.Skipped[1].Limit)
	}
}
