package secrets

import (
	"bytes"
	"slices"
)

// MaxLineBytes bounds how much of a single line is examined. Minified bundles,
// lock files, and embedded data arrive as one enormous line; a credential is
// not hiding at column 40000, and scanning the whole thing costs far more than
// it finds.
const MaxLineBytes = 4096

// MaxHitsPerFile bounds how many matches one file contributes. A leaked
// private key file matches on every one of its lines; the first few say
// everything the twentieth would.
const MaxHitsPerFile = 20

// binarySniff is how much of a file is examined for the NUL byte that marks it
// as binary, matching git's own heuristic.
const binarySniff = 8000

// Scan reports every rule match in content, as line numbers and rule
// identifiers. It returns no part of what it read.
//
// path selects which rules apply — the dotenv assignment rule is scoped by
// filename — and is otherwise unused.
func Scan(path string, content []byte) []Hit {
	if IsBinary(content) {
		return nil
	}

	active := make([]rule, 0, len(builtin))
	for _, r := range builtin {
		if r.files == nil || r.files(path) {
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		return nil
	}

	var hits []Hit
	line := 0
	for rest := content; len(rest) > 0; {
		var text []byte
		if before, after, ok := bytes.Cut(rest, []byte{'\n'}); ok {
			text, rest = before, after
		} else {
			text, rest = rest, nil
		}
		line++

		if len(text) > MaxLineBytes {
			text = text[:MaxLineBytes]
		}
		for _, r := range active {
			if !matches(r, text) {
				continue
			}
			hits = append(hits, Hit{Rule: r.id, Line: line})
			if len(hits) >= MaxHitsPerFile {
				return hits
			}
		}
	}
	return hits
}

// matches reports whether the rule fires on one line. A rule with a verifier
// is given every match on the line, since the first may be a placeholder while
// a later one is not.
func matches(r rule, text []byte) bool {
	if r.verify == nil {
		return r.re.Match(text)
	}
	return slices.ContainsFunc(r.re.FindAllSubmatch(text, -1), r.verify)
}

// IsBinary reports whether content should be treated as binary and left
// unscanned, using git's heuristic of a NUL byte near the start.
func IsBinary(content []byte) bool {
	head := content
	if len(head) > binarySniff {
		head = head[:binarySniff]
	}
	return bytes.IndexByte(head, 0) >= 0
}
