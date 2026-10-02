package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/hubcheck"
	"github.com/madelinecameron/poppy/internal/model"
)

func runHubCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hub-check", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository path on the hub host, overriding the one derived from hubRemote")
	host := fs.String("host", "", "ssh target on the hub host, overriding hubCheck.host")
	local := fs.Bool("local", false, "check a repository on this machine rather than over ssh")
	noFsck := fs.Bool("no-fsck", false, "sweep for zero-length files without running git fsck")
	asJSON := fs.Bool("json", false, "emit the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfigOnly()
	if err != nil {
		return err
	}

	opts, reason := cfg.HubCheckTarget()
	if *repo != "" {
		opts.Repo = *repo
		reason = ""
	}
	if *host != "" {
		opts.Host = *host
		reason = ""
	}
	if *local {
		opts.Host = ""
		reason = ""
	}
	if reason != "" {
		return fmt.Errorf("%s — set hubCheck.host in %s, or pass --repo and --host", reason, config.Path())
	}
	opts.Fsck = !*noFsck
	opts.FsckSkipReason = ""
	if *noFsck {
		opts.FsckSkipReason = "--no-fsck was passed"
	}

	result := hubcheck.Run(ctx, opts)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	printHubResult(os.Stdout, result)
	// Anything short of a clean result exits non-zero, corruption and silence
	// alike. A check that could not run is not a pass, and a caller that only
	// reads the exit status must not be told otherwise.
	if result.Verdict() != hubcheck.StatusClean {
		return exitCode(1)
	}
	return nil
}

// printHubResult renders one machine's check in full, for someone who ran it
// by hand and wants everything it saw.
func printHubResult(w io.Writer, r *hubcheck.Result) {
	where := r.Repo
	if r.Host != "" {
		where += " on " + r.Host
	}
	fmt.Fprintf(w, "%s  %s  %s\n", paint(bold, "HUB"), hubVerdict(r.Verdict()), paint(dim, where))
	if r.Reason != "" {
		fmt.Fprintf(w, "  %s\n", r.Reason)
	}

	fmt.Fprintf(w, "  %-6s %s\n", "sweep", hubVerdict(r.Sweep.Status))
	for _, p := range r.Zero {
		fmt.Fprintf(w, "    %s %s\n", paint(red, "zero-length"), p)
	}
	if r.Sweep.Truncated {
		fmt.Fprintf(w, "    %s\n", paint(dim, fmt.Sprintf("… %d more not listed", r.ZeroCount-len(r.Zero))))
	}
	for _, d := range r.Sweep.Detail {
		fmt.Fprintf(w, "    %s\n", paint(dim, d))
	}

	fmt.Fprintf(w, "  %-6s %s\n", "fsck", hubVerdict(r.Fsck.Status))
	for _, d := range r.Fsck.Detail {
		fmt.Fprintf(w, "    %s\n", paint(dim, d))
	}
}

// hubVerdict paints a status. Every way of not having looked is painted as
// something to act on, because a check that never ran says nothing
// about the hub's health.
func hubVerdict(s hubcheck.Status) string {
	switch s {
	case hubcheck.StatusClean:
		return paint(green, "clean")
	case hubcheck.StatusFound:
		return paint(bold+red, "CORRUPT")
	case hubcheck.StatusNotConfigured:
		return paint(dim, "not configured")
	default:
		return paint(bold+yellow, "NOT CHECKED ("+s.String()+")")
	}
}

// printHubIntegrity is the fleet-level line in "poppy status".
//
// It always prints when any machine should be checking, and it always prints
// the count of machines that are not, alongside the verdict rather than folded
// into it. A clean hub with a machine that could not look is two facts, and
// showing only the first hides the second.
func printHubIntegrity(w io.Writer, h model.HubIntegrity, now time.Time) {
	if len(h.Reports) == 0 {
		return
	}

	line := hubVerdict(h.Verdict)
	if h.Ran > 0 {
		line += paint(dim, fmt.Sprintf(" · checked %s", relTime(h.LastRan, now)))
	}
	if h.Silent > 0 {
		line += "  " + paint(bold+yellow, fmt.Sprintf("%s could not check", plural(h.Silent, "machine")))
	}
	fmt.Fprintf(w, "%s  %s\n", paint(bold, "HUB"), line)

	for _, report := range h.Reports {
		verdict := report.Verdict()
		if verdict == hubcheck.StatusClean {
			continue
		}
		result := report.Result
		if result == nil {
			// A machine publishing a document with no check in it at all,
			// from an older poppy. Named rather than passed over, because the
			// missing check is itself the finding.
			fmt.Fprintf(w, "  %s  %s  %s\n", paint(cyan, report.Machine), hubVerdict(verdict),
				paint(dim, "this machine records no hub check; its poppy predates one"))
			continue
		}

		detail := result.Reason
		if detail == "" {
			detail = fmt.Sprintf("sweep %s · fsck %s", result.Sweep.Status, result.Fsck.Status)
		}
		fmt.Fprintf(w, "  %s  %s  %s\n",
			paint(cyan, report.Machine), hubVerdict(verdict), paint(dim, detail))

		for _, p := range result.Zero {
			fmt.Fprintf(w, "    %s %s\n", paint(red, "zero-length"), p)
		}
		if result.Sweep.Truncated {
			fmt.Fprintf(w, "    %s\n",
				paint(dim, fmt.Sprintf("… %d more not listed", result.ZeroCount-len(result.Zero))))
		}
		for _, d := range result.Fsck.Detail {
			if strings.TrimSpace(d) == "" {
				continue
			}
			fmt.Fprintf(w, "    %s\n", paint(dim, d))
		}
	}
}
