package snapshot

import (
	"strconv"
	"strings"
	"time"
)

// Ref namespaces. Poppy writes only beneath refs/poppy, so it can never
// disturb a branch, a tag, or anything else git or the user relies on.
const (
	// LatestPrefix holds the most recent snapshot per branch.
	LatestPrefix = "refs/poppy/wip"
	// HistoryPrefix holds every retained snapshot, keyed by capture time.
	HistoryPrefix = "refs/poppy/snap"
)

// encodeBranch makes a branch name safe as a single ref path component.
//
// Branch names may contain slashes, so embedding one unescaped would make
// refs/poppy/snap/<m>/feature/<time> and refs/poppy/snap/<m>/feature/auth/<time>
// collide: git cannot store a ref at a path that is also a directory. Escaping
// the separator keeps every branch on one level and removes the conflict.
func encodeBranch(branch string) string {
	branch = strings.ReplaceAll(branch, "%", "%25")
	return strings.ReplaceAll(branch, "/", "%2F")
}

func decodeBranch(component string) string {
	component = strings.ReplaceAll(component, "%2F", "/")
	return strings.ReplaceAll(component, "%25", "%")
}

// LatestRef names the ref tracking the newest snapshot of a branch.
func LatestRef(machine, branch string) string {
	return LatestPrefix + "/" + machine + "/" + encodeBranch(branch)
}

// HistoryRef names the ref holding one snapshot taken at a moment in time.
//
// The moment is recorded in milliseconds. Seconds are too coarse: two captures
// within one second would name the same ref, and the second would silently
// replace the first rather than being recorded alongside it.
func HistoryRef(machine, branch string, taken time.Time) string {
	return HistoryPrefix + "/" + machine + "/" + encodeBranch(branch) +
		"/" + strconv.FormatInt(taken.UnixMilli(), 10)
}

// MachinePattern matches every ref a machine owns, which is the only namespace
// it is permitted to write.
func MachinePattern(machine string) string {
	return "refs/poppy/*/" + machine + "/*"
}

// parseHistoryRef recovers the branch and capture time from a history ref.
func parseHistoryRef(ref string) (machine, branch string, taken time.Time, ok bool) {
	rest, found := strings.CutPrefix(ref, HistoryPrefix+"/")
	if !found {
		return "", "", time.Time{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return "", "", time.Time{}, false
	}
	millis, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", time.Time{}, false
	}
	return parts[0], decodeBranch(parts[1]), time.UnixMilli(millis), true
}
