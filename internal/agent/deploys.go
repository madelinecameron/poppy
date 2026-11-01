package agent

import (
	"context"
	"time"

	"github.com/madelinecameron/poppy/internal/deploys"
)

// publishDeploys prunes the local deploy store and publishes what remains.
func (a *Agent) publishDeploys(ctx context.Context) (int, error) {
	if a.DeploysDir == "" {
		return 0, nil
	}
	a.deploysSeen = deploys.Changed(a.DeploysDir)
	if n, err := deploys.Prune(a.DeploysDir, time.Now(), deploys.PruneAfter); err != nil {
		a.Log.Warn("deploy prune failed", "err", err)
	} else if n > 0 {
		a.Log.Info("pruned deploys", "count", n)
	}
	records, err := deploys.List(a.DeploysDir)
	if err != nil {
		return 0, err
	}
	for i := range records {
		if records[i].Machine == "" {
			records[i].Machine = a.Config.Machine
		}
	}
	_, err = a.Store.PublishDeploys(ctx, a.Config.Machine, records)
	return len(records), err
}

// publishChangedDeploys publishes between scans when a deploy has arrived
// since the last publish.
func (a *Agent) publishChangedDeploys(ctx context.Context) {
	if a.DeploysDir == "" || !deploys.Changed(a.DeploysDir).After(a.deploysSeen) {
		return
	}
	n, err := a.publishDeploys(ctx)
	if err != nil {
		a.Log.Warn("deploy publish failed", "deploys", n, "err", err)
		return
	}
	a.Log.Info("deploys published", "deploys", n)
}
