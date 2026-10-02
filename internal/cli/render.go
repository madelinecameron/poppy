package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Colors are disabled when output is not a terminal or NO_COLOR is set, so
// piped output stays clean.
var useColor = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	blue   = "\x1b[34m"
	cyan   = "\x1b[36m"
)

func paint(code, s string) string {
	if !useColor || s == "" {
		return s
	}
	return code + s + reset
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// width returns the printed width of s, ignoring color escapes.
func width(s string) int {
	return utf8.RuneCountInString(ansiRE.ReplaceAllString(s, ""))
}

// table accumulates rows and prints them in aligned columns. It measures
// printed width rather than byte length so colored cells align correctly.
type table struct {
	header []string
	rows   [][]string
}

func (t *table) setHeader(cols ...string) { t.header = cols }

func (t *table) add(cols ...string) { t.rows = append(t.rows, cols) }

func (t *table) empty() bool { return len(t.rows) == 0 }

func (t *table) render(w io.Writer, indent string) {
	all := t.rows
	if t.header != nil {
		all = append([][]string{t.header}, t.rows...)
	}

	widths := map[int]int{}
	for _, row := range all {
		for i, cell := range row {
			if n := width(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}

	if t.header != nil {
		fmt.Fprint(w, indent)
		printRow(w, t.header, widths, dim)
	}
	for _, row := range t.rows {
		fmt.Fprint(w, indent)
		printRow(w, row, widths, "")
	}
}

func printRow(w io.Writer, row []string, widths map[int]int, style string) {
	var b strings.Builder
	for i, cell := range row {
		text := cell
		if style != "" {
			text = paint(style, cell)
		}
		b.WriteString(text)
		if i < len(row)-1 {
			b.WriteString(strings.Repeat(" ", widths[i]-width(cell)+2))
		}
	}
	fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
}

// timeNow is the clock used across CLI rendering, wrapped so it reads clearly
// at call sites.
func timeNow() time.Time { return time.Now() }

// relTime renders a duration since t in a compact, scannable form.
func relTime(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := max(now.Sub(t), 0)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// shortAge renders a duration as a bare magnitude for dense columns.
func shortAge(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ageColor escalates from neutral to red as stranded work gets older, so the
// oldest untracked work is the most visually urgent.
func ageColor(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d > 72*time.Hour:
		return red
	case d > 24*time.Hour:
		return yellow
	default:
		return ""
	}
}

func plural(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	suffix := "s"
	for _, sibilant := range []string{"ch", "sh", "s", "x", "z"} {
		if strings.HasSuffix(singular, sibilant) {
			suffix = "es"
			break
		}
	}
	return fmt.Sprintf("%d %s%s", n, singular, suffix)
}
