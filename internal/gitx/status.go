package gitx

import (
	"context"
	"strconv"
	"strings"

	"github.com/mads/poppy/internal/model"
)

// MaxDirtyFiles bounds how many paths a state document lists.
const MaxDirtyFiles = 200

// Status is the parsed result of `git status --porcelain=v2`.
type Status struct {
	Head     model.Head
	Upstream string
	Ahead    int
	Behind   int
	Dirty    model.Dirty
}

// Status runs status and parses porcelain v2 output.
func (r *Runner) Status(ctx context.Context) (Status, error) {
	fields, err := r.RunZ(ctx, "status", "--porcelain=v2", "--branch", "--untracked-files=normal", "-z")
	if err != nil {
		return Status{}, err
	}
	return parseStatus(fields), nil
}

// parseStatus consumes NUL-delimited porcelain v2 records. Rename and copy
// records (type 2) carry their original path in a following field, which must
// be consumed so it is not mistaken for a record of its own.
func parseStatus(fields []string) Status {
	var s Status
	var files []string

	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			continue
		}

		switch rec[0] {
		case '#':
			parseHeader(rec, &s)

		case '1', '2', 'u':
			if len(rec) < 4 {
				continue
			}
			x, y := rec[2], rec[3]
			path := lastSpaceField(rec)

			if rec[0] == 'u' {
				s.Dirty.Unmerged++
			} else {
				if x != '.' {
					s.Dirty.Staged++
				}
				if y != '.' {
					s.Dirty.Unstaged++
				}
			}
			if rec[0] == '2' {
				i++ // original path of a rename or copy
			}
			files = append(files, path)

		case '?':
			s.Dirty.Untracked++
			files = append(files, strings.TrimPrefix(rec, "? "))
		}
	}

	if len(files) > MaxDirtyFiles {
		files = files[:MaxDirtyFiles]
		s.Dirty.Truncated = true
	}
	s.Dirty.Files = files
	return s
}

func parseHeader(rec string, s *Status) {
	const prefix = "# "
	body := strings.TrimPrefix(rec, prefix)
	key, value, ok := strings.Cut(body, " ")
	if !ok {
		return
	}

	switch key {
	case "branch.oid":
		if value == "(initial)" {
			s.Head.Unborn = true
			return
		}
		s.Head.SHA = value
	case "branch.head":
		if value == "(detached)" {
			s.Head.Detached = true
			return
		}
		s.Head.Branch = value
	case "branch.upstream":
		s.Upstream = value
	case "branch.ab":
		ahead, behind, ok := strings.Cut(value, " ")
		if !ok {
			return
		}
		s.Ahead = parseSigned(ahead)
		s.Behind = parseSigned(behind)
	}
}

// parseSigned reads git's "+3" / "-2" tracking counts as magnitudes.
func parseSigned(s string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-"))
	if err != nil {
		return 0
	}
	return n
}

// lastSpaceField returns the path portion of a porcelain v2 entry, which is
// everything after the final space-delimited metadata column. Paths may
// themselves contain spaces, so the column count is fixed per record type.
func lastSpaceField(rec string) string {
	columns := map[byte]int{'1': 8, '2': 9, 'u': 10}[rec[0]]
	if columns == 0 {
		return rec
	}
	rest := rec
	for range columns {
		_, after, ok := strings.Cut(rest, " ")
		if !ok {
			return rest
		}
		rest = after
	}
	return rest
}
