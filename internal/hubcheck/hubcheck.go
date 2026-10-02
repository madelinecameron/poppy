// Package hubcheck examines the hub repository's files on the host that holds
// them, rather than asking git about it from somewhere else.
//
// On 20 August a host restart mid-push left thirteen zero-length objects in
// /opt/fleet/poppy-hub.git. Git degraded around them quietly: it warned once
// about a pack index, ignored the bad pack, and resolved refs anyway. Every
// git command poppy ran succeeded, so the scan had nothing to report, and the
// hub received nothing for sixteen days before anyone noticed. Git is the
// thing that hid it, so the load-bearing check here is a filesystem sweep for
// zero-length files in the object store; "git fsck --full --strict" is the
// corroborating second opinion, not the primary signal.
//
// Both checks run as one POSIX shell script, executed either on this machine
// or over ssh on the hub host. One script means the remote path and the local
// path cannot drift apart, and it means a future poppy installed on the hub
// host runs the same check with Host left empty.
package hubcheck

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/shell"
)

// DefaultTimeout bounds one whole check. A full --strict fsck of the 136MB
// fleet hub measured 16.5s, so the bound is generous enough that a slow run is
// reported as what it found rather than as a timeout.
const DefaultTimeout = 2 * time.Minute

// connectTimeoutSeconds bounds how long ssh waits to reach a hub host that is
// not answering. The check runs inside the scan cycle, so a host that is down
// must fail fast and be reported as unchecked rather than delay the scan.
const connectTimeoutSeconds = 10

// maxDetail bounds how many lines of evidence one check reports. A repository
// broken badly enough to emit more than this has already made its point, and
// the bound keeps a runaway fsck from filling the hub document.
const maxDetail = 200

// Status is one check's outcome.
//
// It is deliberately not a boolean. A boolean has two states and this has
// three, and the third — "this check did not run" — is the one the incident
// turned on: a scan that never looked reported exactly what a scan that looked
// and found nothing reported.
type Status string

const (
	// StatusUnknown is the zero value, and it means the check did not run.
	// The zero value is this one on purpose: a Result nobody filled in, a
	// document from a poppy too old to carry one, and a check that died before
	// it could look all land here rather than on StatusClean.
	StatusUnknown Status = ""
	// StatusClean means the check ran and found nothing wrong.
	StatusClean Status = "clean"
	// StatusFound means the check ran and found corruption.
	StatusFound Status = "found"
	// StatusFailed means the check tried to run and could not — the host was
	// unreachable, the path was not a repository, git was missing.
	StatusFailed Status = "failed"
	// StatusSkipped means the check was deliberately not run this cycle,
	// which is benign but is still not a clean result.
	StatusSkipped Status = "skipped"
	// StatusNotConfigured means this machine has no hub host to check. A
	// local-only poppy is not failing to check the fleet hub; it has no fleet
	// hub, and saying so is different from saying it looked.
	StatusNotConfigured Status = "not-configured"
)

// Ran reports whether the check actually examined the repository. Only Clean
// and Found did; every other status is some flavour of absence.
func (s Status) Ran() bool { return s == StatusClean || s == StatusFound }

// String names the status for display, giving the zero value a word of its own
// so an unfilled check never renders as blank next to a real answer.
func (s Status) String() string {
	if s == StatusUnknown {
		return "not-run"
	}
	return string(s)
}

// Check is one of the two checks and what it saw.
type Check struct {
	// Status is never omitted from the document. An absent field would decode
	// to the same zero value as a check that ran, which is the confusion this
	// whole type exists to prevent, so it is written even when empty.
	Status Status `json:"status"`
	// Detail is what the check said, bounded by maxDetail.
	Detail []string `json:"detail,omitempty"`
	// Truncated reports that Detail is shorter than what the check produced.
	Truncated bool `json:"truncated,omitempty"`
}

// Result is one attempt at checking the hub repository.
type Result struct {
	// Repo and Host name what was examined and where, so a reader can tell
	// which repository on which machine a verdict is about.
	Repo string `json:"repo,omitempty"`
	Host string `json:"host,omitempty"`
	// CheckedAt is when the attempt was made — not when it succeeded. An
	// attempt that failed still records the time, because "last tried at" is
	// what says whether anything is happening at all.
	CheckedAt time.Time `json:"checkedAt"`
	// HostBoot identifies the hub host's current boot. A change since the
	// previous check means the host restarted in between, which is the
	// condition that produced the corruption, so it forces a full fsck.
	HostBoot string `json:"hostBoot,omitempty"`
	// Reason says why a check did not run, in words meant for a person.
	Reason string `json:"reason,omitempty"`

	Sweep Check `json:"sweep"`
	Fsck  Check `json:"fsck"`

	// Zero are the zero-length files the sweep found, and ZeroCount how many
	// there were before Zero was bounded.
	Zero      []string `json:"zero,omitempty"`
	ZeroCount int      `json:"zeroCount"`
}

// NotRun returns a Result for a check that never reached the repository, with
// both checks carrying the same status and reason. Callers that decline to
// check still return one of these rather than nil, so that "poppy chose not to
// look" is a recorded fact and not an empty space in the document.
func NotRun(status Status, reason string) *Result {
	return &Result{
		CheckedAt: time.Now().UTC(),
		Reason:    reason,
		Sweep:     Check{Status: status},
		Fsck:      Check{Status: status},
	}
}

// Verdict is the single word for a Result.
//
// It returns StatusClean only when both checks actually ran, which is the
// whole point: a result is clean because something looked and found nothing,
// never because nothing looked. A nil Result — a machine whose document
// predates this check — is StatusUnknown, not health.
func (r *Result) Verdict() Status {
	if r == nil {
		return StatusUnknown
	}
	switch {
	case r.Sweep.Status == StatusFound || r.Fsck.Status == StatusFound:
		return StatusFound
	case r.Sweep.Status.Ran() && r.Fsck.Status.Ran():
		return StatusClean
	case r.Sweep.Status == StatusFailed || r.Fsck.Status == StatusFailed:
		return StatusFailed
	case r.Sweep.Status == StatusNotConfigured || r.Fsck.Status == StatusNotConfigured:
		return StatusNotConfigured
	case r.Sweep.Status == StatusSkipped || r.Fsck.Status == StatusSkipped:
		return StatusSkipped
	default:
		return StatusUnknown
	}
}

// Corrupt reports whether either check found corruption.
func (r *Result) Corrupt() bool { return r.Verdict() == StatusFound }

// Options controls one check.
type Options struct {
	// Repo is the repository's path on the hub host.
	Repo string
	// Host is the ssh target to run on. Empty runs on this machine, which is
	// what a poppy installed on the hub host itself would do.
	//
	// This must be an account with a real shell. The hub's own remote URL
	// names an account whose shell is git-shell, which answers git commands
	// and rejects everything else, so it cannot run either check.
	Host string
	// Fsck runs "git fsck --full --strict". When false the fsck is reported
	// as skipped, with FsckSkipReason saying why, rather than omitted.
	Fsck bool
	// FsckSkipReason explains a false Fsck to a person reading the document.
	FsckSkipReason string
	// PrevBoot is the host boot recorded by the previous check. When the host
	// has restarted since, the fsck runs whatever Fsck says.
	PrevBoot string
	// Timeout bounds the whole check.
	Timeout time.Duration
}

// Run performs the check and returns what it found.
//
// It returns no error. Every failure is a Status and a Reason inside the
// Result, because an error returned upward is a thing a caller can drop, and a
// dropped error here is indistinguishable from a healthy hub.
func Run(ctx context.Context, opts Options) *Result {
	r := &Result{
		Repo:      opts.Repo,
		Host:      opts.Host,
		CheckedAt: time.Now().UTC(),
	}
	if opts.Repo == "" {
		r.Reason = "no hub repository path to check"
		r.Sweep.Status = StatusNotConfigured
		r.Fsck.Status = StatusNotConfigured
		return r
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, name(opts.Host), args(opts)...).Output()
	if err != nil {
		r.Reason = execReason(opts.Host, err)
		r.Sweep.Status = StatusFailed
		r.Fsck.Status = StatusFailed
		return r
	}

	parse(r, string(out))
	if !opts.Fsck && r.Fsck.Status == StatusSkipped && opts.FsckSkipReason != "" {
		r.Fsck.Detail = []string{opts.FsckSkipReason}
	}
	return r
}

func name(host string) string {
	if host == "" {
		return "sh"
	}
	return "ssh"
}

func args(opts Options) []string {
	script := Script(opts.Repo, opts.Fsck, opts.PrevBoot)
	if opts.Host == "" {
		return []string{"-c", script}
	}
	// BatchMode and accept-new are the flags mirror already uses to reach this
	// host: never prompt, and record an unknown host key but still refuse a
	// changed one. ConnectTimeout is this check's own: it runs on a timer
	// inside the scan cycle, and ssh's default wait for a host that is simply
	// gone is long enough to hold up the scan behind it.
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=" + strconv.Itoa(connectTimeoutSeconds),
		opts.Host, script,
	}
}

func execReason(host string, err error) string {
	where := "on this machine"
	if host != "" {
		where = "on " + host
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if msg := strings.TrimSpace(string(exit.Stderr)); msg != "" {
			return fmt.Sprintf("could not run the check %s: %s", where, firstLine(msg))
		}
	}
	return fmt.Sprintf("could not run the check %s: %v", where, err)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// parse reads the line protocol Script emits.
//
// The header and the "end" sentinel are both required. Output that stops in
// the middle — an ssh connection dropped mid-stream, a killed remote shell —
// otherwise arrives as a short but well-formed report, and a report that ends
// early after a clean sweep line would read as a clean hub. Refusing anything
// without its sentinel is what makes a truncated answer an absent one.
func parse(r *Result, out string) {
	var (
		sawHeader bool
		sawEnd    bool
	)
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimRight(line, "\r"), " ")
		switch key {
		case "poppy-hubcheck":
			sawHeader = true
		case "end":
			sawEnd = true
		case "host":
			if r.Host == "" {
				r.Host = value
			}
		case "boot":
			r.HostBoot = value
		case "sweep":
			r.Sweep.Status = Status(value)
		case "fsck":
			r.Fsck.Status = Status(value)
		case "zero":
			r.Zero = append(r.Zero, value)
		case "zero-count":
			r.ZeroCount, _ = strconv.Atoi(value)
		case "sweep-detail":
			r.Sweep.Detail = append(r.Sweep.Detail, value)
		case "fsck-detail":
			r.Fsck.Detail = append(r.Fsck.Detail, value)
		}
	}

	if !sawHeader || !sawEnd {
		r.Reason = "the check produced no complete report; treating it as not run"
		r.Sweep = Check{Status: StatusFailed}
		r.Fsck = Check{Status: StatusFailed}
		r.Zero = nil
		r.ZeroCount = 0
		return
	}

	r.Sweep.Truncated = r.ZeroCount > len(r.Zero)
	if r.Sweep.Status == StatusFound {
		r.Reason = fmt.Sprintf("%d zero-length file(s) in the object store", r.ZeroCount)
	}
}

// Script is the check, as one POSIX shell script.
//
// It is a script rather than a sequence of commands because the hub host is
// reached over ssh, and one round trip that returns everything is the only
// shape in which "the connection died halfway" is distinguishable from "the
// repository is fine" — see parse.
func Script(repo string, fsck bool, prevBoot string) string {
	want := "0"
	if fsck {
		want = "1"
	}
	return fmt.Sprintf(`set -u
repo=%s
want_fsck=%s
prev_boot=%s

emit() {
  while IFS= read -r line; do
    [ -n "$line" ] && printf '%%s %%s\n' "$1" "$line"
  done
}

printf 'poppy-hubcheck 1\n'
printf 'host %%s\n' "$(uname -n 2>/dev/null || echo unknown)"

boot=''
if [ -r /proc/sys/kernel/random/boot_id ]; then
  boot=$(cat /proc/sys/kernel/random/boot_id 2>/dev/null || echo '')
  [ -n "$boot" ] && printf 'boot %%s\n' "$boot"
fi
# Any restart of the hub host forces the full fsck, not only an unclean one.
# Nothing readable from here says whether a stop was graceful, and treating
# every restart as suspect is the superset that cannot miss the unclean case.
if [ -n "$prev_boot" ] && [ -n "$boot" ] && [ "$prev_boot" != "$boot" ]; then
  want_fsck=1
  printf 'fsck-detail forced: the hub host restarted since the last check\n'
fi

if [ ! -d "$repo" ]; then
  printf 'sweep failed\n'
  printf 'sweep-detail no repository at %%s\n' "$repo"
  printf 'fsck failed\n'
  printf 'fsck-detail no repository at %%s\n' "$repo"
  printf 'end\n'
  exit 0
fi

# The sweep. This is the check that would have caught the incident: the bad
# objects sat in a pack git had decided to ignore, so nothing that went through
# git could see them, but every one of them was a zero-length file on disk.
if zeros=$(find "$repo/objects" -type f -size 0 2>&1); then
  count=$(printf '%%s\n' "$zeros" | grep -c . || true)
  printf 'zero-count %%s\n' "$count"
  if [ "$count" -gt 0 ]; then
    printf 'sweep found\n'
    printf '%%s\n' "$zeros" | head -n %d | emit zero
  else
    printf 'sweep clean\n'
  fi
else
  printf 'sweep failed\n'
  printf '%%s\n' "$zeros" | head -n %d | emit sweep-detail
fi

if [ "$want_fsck" != "1" ]; then
  printf 'fsck skipped\n'
elif ! command -v git >/dev/null 2>&1; then
  printf 'fsck failed\n'
  printf 'fsck-detail git is not on PATH on this host\n'
elif ! git --git-dir="$repo" rev-parse --git-dir >/dev/null 2>&1; then
  # rev-parse reads no objects, so it still succeeds on a repository whose
  # object store is wrecked. Failing here means the path is not a repository
  # this account may read at all, which is a different problem and not a
  # clean bill of health.
  printf 'fsck failed\n'
  printf 'fsck-detail %%s is not a git repository this account can read\n' "$repo"
else
  if out=$(git --git-dir="$repo" fsck --full --strict 2>&1); then
    printf 'fsck clean\n'
  else
    printf 'fsck found\n'
    printf '%%s\n' "$out" | head -n %d | emit fsck-detail
  fi
fi

printf 'end\n'
`, shell.Quote(repo), want, shell.Quote(prevBoot), maxDetail, maxDetail, maxDetail)
}

// RepoPathFromRemote extracts the repository's path on its host from a hub
// remote URL, so the check examines the same repository poppy publishes to
// without a second setting to keep in step.
//
// It understands the ssh:// form the fleet contract uses, the scp-style
// host:path form git also accepts, and a plain local path.
func RepoPathFromRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	switch {
	case remote == "":
		return ""
	case strings.HasPrefix(remote, "ssh://"):
		rest := strings.TrimPrefix(remote, "ssh://")
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			return ""
		}
		return rest[i:]
	case strings.Contains(remote, "://"):
		return "" // http and git protocols expose no filesystem path
	case !strings.Contains(remote, ":"):
		return remote // a local path
	default:
		_, path, _ := strings.Cut(remote, ":")
		return path
	}
}
