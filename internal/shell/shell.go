// Package shell builds POSIX shell command lines.
package shell

import "strings"

// Quote returns s as a single-quoted shell word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
