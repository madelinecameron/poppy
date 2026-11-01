package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/madelinecameron/poppy/internal/deploys"
	"github.com/madelinecameron/poppy/internal/model"
)

// maxDeployRecord bounds what deploy-record reads from stdin.
const maxDeployRecord = 64 << 10

// runDeployRecord keeps one houseparty deploy record, read from stdin, for the
// agent to publish. Unlike session-hook it fails loudly: houseparty is the
// only caller and reports the error as a warning.
func runDeployRecord(_ context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: poppy deploy-record < record.json")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxDeployRecord))
	if err != nil {
		return err
	}
	d, err := deploys.Parse(data)
	if err != nil {
		return err
	}
	return deploys.Save(deploys.Dir(), d, time.Now())
}

func runDeploys(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("deploys", flag.ContinueOnError)
	since := fs.Duration("since", 30*24*time.Hour, "only deploys this recent")
	project := fs.String("project", "", "only deploys of this project")
	env := fs.String("env", "", "only deploys to this env")
	service := fs.String("service", "", "only deploys of this service")
	machine := fs.String("machine", "", "only deploys run from this machine")
	failed := fs.Bool("failed", false, "only deploys that failed")
	limit := fs.Int("limit", 0, "show at most this many, newest first")
	asJSON := fs.Bool("json", false, "print JSON")
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, store, err := setupRead(ctx)
	if err != nil {
		return err
	}
	if !*noPull {
		store.Pull(ctx)
	}
	fleet, err := store.Fleet()
	if err != nil {
		return err
	}

	now := time.Now()
	list := fleet.FilterDeploys(model.DeployFilter{
		Since: now.Add(-*since), Project: *project, Env: *env, Service: *service,
		Machine: *machine, Failed: *failed, Limit: *limit,
	})
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	printDeploys(os.Stdout, list, now)
	return nil
}

func printDeploys(w io.Writer, list []model.Deploy, now time.Time) {
	if len(list) == 0 {
		fmt.Fprintln(w, "No deploys match.")
		return
	}
	failed := 0
	for _, d := range list {
		if !d.OK {
			failed++
		}
	}
	fmt.Fprintf(w, "%s  %s · %s\n\n", paint(bold, "DEPLOYS"), plural(len(list), "deploy"),
		paint(red, fmt.Sprintf("%d failed", failed)))
	var t table
	t.setHeader("WHEN", "SERVICE", "RESULT", "REVISION", "TOOK", "MACHINE")
	for _, d := range list {
		result := paint(green, "ok")
		if !d.OK {
			result = paint(red, "failed")
		}
		rev := d.Revision
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if rev == "" {
			rev = paint(dim, "-")
		}
		t.add(relTime(d.Time, now), d.Ref(), result, rev, d.Duration.Round(time.Second).String(), d.Machine)
	}
	t.render(w, "")
}
