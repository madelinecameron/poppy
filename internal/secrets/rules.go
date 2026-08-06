package secrets

import (
	"bytes"
	"math"
	"path"
	"regexp"
	"strings"
)

// The built-in rule set is deliberately small. It has to survive 138
// repositories without being switched off, and a scanner people mute is worth
// nothing, so every rule here matches a credential shape that is either
// self-identifying (a token with a vendor prefix) or corroborated by the name
// it is assigned to. Recall is traded away on purpose: gitleaks is available
// as a stronger backend for anyone who wants the long tail.
const (
	RulePrivateKey      RuleID = "private-key"
	RuleAWSAccessKeyID  RuleID = "aws-access-key-id"
	RuleAWSSecretKey    RuleID = "aws-secret-access-key"
	RuleGitHubToken     RuleID = "github-token"
	RuleAnthropicKey    RuleID = "anthropic-api-key"
	RuleProviderKey     RuleID = "provider-api-key"
	RuleSlackToken      RuleID = "slack-token"
	RuleSlackWebhook    RuleID = "slack-webhook"
	RuleStripeSecretKey RuleID = "stripe-secret-key"
	RuleEnvAssignment   RuleID = "env-secret-assignment"
)

// rule is one detection pattern.
type rule struct {
	id RuleID
	re *regexp.Regexp
	// files limits the rule to the paths it accepts; nil applies it everywhere.
	files func(p string) bool
	// verify rejects a match the pattern alone cannot tell from a placeholder.
	// It is a predicate over the matched bytes: those bytes stay inside this
	// package, and what leaves verify is yes or no.
	verify func(m [][]byte) bool
}

var builtin = []rule{
	{
		id: RulePrivateKey,
		re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`),
	},
	{
		id:     RuleAWSAccessKeyID,
		re:     regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		verify: notExample,
	},
	{
		id: RuleAWSSecretKey,
		re: regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})`),
		verify: func(m [][]byte) bool {
			return notExample(m) && entropy(m[1]) >= 3.5
		},
	},
	{
		id: RuleGitHubToken,
		re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{60,255})\b`),
	},
	{
		id: RuleAnthropicKey,
		re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{24,}`),
	},
	{
		id: RuleProviderKey,
		re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{32,}`),
		verify: func(m [][]byte) bool {
			// Anthropic keys are this shape too and have their own rule; one
			// credential should be reported once.
			return !bytes.HasPrefix(m[0], []byte("sk-ant-")) && entropy(m[0]) >= 3.0
		},
	},
	{
		id:     RuleSlackToken,
		re:     regexp.MustCompile(`\bxox[abeprs]-[A-Za-z0-9-]{10,}`),
		verify: func(m [][]byte) bool { return entropy(m[0]) >= 2.5 },
	},
	{
		id: RuleSlackWebhook,
		re: regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9]{6,}/B[A-Za-z0-9]{6,}/[A-Za-z0-9]{16,}`),
	},
	{
		id: RuleStripeSecretKey,
		re: regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{16,}\b`),
	},
	{
		id:     RuleEnvAssignment,
		files:  envShaped,
		re:     regexp.MustCompile(`^[ \t]*(?:export[ \t]+)?([A-Za-z][A-Za-z0-9_]*)[ \t]*=[ \t]*(.*)$`),
		verify: secretAssignment,
	},
}

// Rules lists the identifiers of every built-in rule, for documentation and
// for the CLI to validate a --rule filter against.
func Rules() []RuleID {
	out := make([]RuleID, 0, len(builtin))
	for _, r := range builtin {
		out = append(out, r.id)
	}
	return out
}

// envShaped reports whether a path is a dotenv file poppy will read
// assignments from. The rule is restricted to these because `SOMETHING_KEY =
// value` is ordinary source code everywhere else and flagging it fleet-wide
// would bury the real findings.
//
// Templates are excluded. A checked-in .env.example holding a generated
// development secret under a name like SESSION_SECRET is indistinguishable
// from a leak by shape alone, and it is the single most common thing a dotenv
// rule gets wrong; the filename is the author saying which it is. The
// self-identifying rules — a private key block, an AWS key id, a live Stripe
// key — still apply to those files, so naming a file .example excuses a
// placeholder and never excuses a real credential.
func envShaped(p string) bool {
	base := path.Base(p)
	if isTemplateName(base) {
		return false
	}
	switch {
	case base == ".env" || base == ".envrc":
		return true
	case strings.HasPrefix(base, ".env."):
		return true
	case strings.HasSuffix(base, ".env"):
		return true
	}
	return false
}

var templateMarkers = []string{".example", ".sample", ".template", ".dist", ".defaults", ".tpl"}

func isTemplateName(base string) bool {
	lower := strings.ToLower(base)
	for _, marker := range templateMarkers {
		if strings.HasSuffix(lower, marker) || strings.Contains(lower, marker+".") {
			return true
		}
	}
	return strings.HasPrefix(lower, "example.") || lower == "env.example"
}

// secretNameSuffixes are the assignment names worth reporting. The name must
// end with one of them, so AWS_SECRET_ACCESS_KEY counts and KEYBOARD_LAYOUT
// does not.
var secretNameSuffixes = []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIALS"}

func secretAssignment(m [][]byte) bool {
	name := strings.ToUpper(string(m[1]))
	if strings.Contains(name, "PUBLIC") {
		return false
	}
	named := false
	for _, suffix := range secretNameSuffixes {
		if strings.HasSuffix(name, suffix) {
			named = true
			break
		}
	}
	return named && plausibleValue(m[2])
}

// placeholders are the substrings that mark a committed dotenv as a template
// rather than a leak. Matching one is the difference between .env.example
// being noise and being 138 false positives.
var placeholders = []string{
	"example", "changeme", "change_me", "change-me", "placeholder", "your-",
	"your_", "yourkey", "yourtoken", "dummy", "redacted", "xxxx", "todo",
	"replaceme", "notarealkey", "insert",
}

// plausibleValue reports whether an assigned value looks like a live
// credential rather than a template, a reference, or a flag.
func plausibleValue(v []byte) bool {
	v = bytes.TrimSpace(v)
	if i := bytes.IndexByte(v, '#'); i > 0 && v[i-1] == ' ' {
		v = bytes.TrimSpace(v[:i])
	}
	v = bytes.Trim(v, `"'`)

	switch {
	case len(v) < 8 || len(v) > 512:
		return false
	case bytes.IndexByte(v, '$') >= 0:
		// Interpolation of another variable, not a value.
		return false
	case bytes.ContainsAny(v, "<>"):
		return false
	}

	lower := bytes.ToLower(v)
	for _, p := range placeholders {
		if bytes.Contains(lower, []byte(p)) {
			return false
		}
	}
	if bytes.Count(v, v[:1]) == len(v) {
		return false
	}
	return entropy(v) >= 3.0
}

// notExample rejects the credential shapes that appear verbatim in vendor
// documentation, of which AKIAIOSFODNN7EXAMPLE is the one that turns up most.
func notExample(m [][]byte) bool {
	return !bytes.Contains(bytes.ToUpper(m[0]), []byte("EXAMPLE"))
}

// entropy is Shannon entropy in bits per byte. Random credentials sit above
// three; prose, identifiers, and repeated filler sit below it.
func entropy(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var counts [256]int
	for _, c := range b {
		counts[c]++
	}
	total := float64(len(b))
	var h float64
	for _, n := range counts {
		if n == 0 {
			continue
		}
		p := float64(n) / total
		h -= p * math.Log2(p)
	}
	return h
}
