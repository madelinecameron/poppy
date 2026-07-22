package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/madelinecameron/poppy/internal/agent"
	"github.com/madelinecameron/poppy/internal/config"
)

func runScan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	quiet := fs.Bool("quiet", false, "suppress the summary line")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, store, err := setup(ctx)
	if err != nil {
		return err
	}

	a := agent.New(cfg, store, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	report, err := a.Cycle(ctx)
	if err != nil {
		return err
	}

	if !*quiet {
		fmt.Printf("%s  %s · %s · %d dirty · %d unpushed · %s\n",
			paint(bold, "SCAN"),
			paint(cyan, cfg.Machine),
			plural(report.Repos, "repo"),
			report.Dirty,
			report.Unpushed,
			paint(dim, report.Duration.Round(time.Millisecond).String()),
		)
		switch {
		case cfg.HubRemote == "":
			fmt.Println(paint(dim, "local-only: set hubRemote in "+config.Path()+" to share with the fleet"))
		case report.Pushed:
			fmt.Println(paint(green, "published to hub"))
		default:
			fmt.Println(paint(yellow, "hub unreachable; queued locally"))
			if report.Err != nil {
				fmt.Println(paint(dim, "  "+report.Err.Error()))
			}
		}
	}
	return nil
}

func runDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", 0, "override the configured scan interval")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, store, err := setup(ctx)
	if err != nil {
		return err
	}
	if *interval > 0 {
		cfg.ScanIntervalSeconds = int(interval.Seconds())
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("poppy daemon starting",
		"machine", cfg.Machine,
		"roots", cfg.Roots,
		"interval", time.Duration(cfg.ScanIntervalSeconds)*time.Second,
		"hub", hubLabel(cfg))

	err = agent.New(cfg, store, log).Run(ctx)
	if ctx.Err() != nil {
		log.Info("poppy daemon stopped")
		return nil
	}
	return err
}

func hubLabel(cfg config.Config) string {
	if cfg.HubRemote == "" {
		return "local-only"
	}
	return cfg.HubRemote
}

func runInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing configuration")
	remote := fs.String("hub", "", "git URL of the shared hub repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if config.Exists() && !*force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", config.Path())
	}

	cfg := config.Defaults()
	if *remote != "" {
		cfg.HubRemote = *remote
	}
	if err := config.Save(cfg); err != nil {
		return err
	}

	fmt.Printf("%s %s\n", paint(green, "wrote"), config.Path())
	fmt.Printf("  machine: %s\n", cfg.Machine)
	fmt.Printf("  roots:   %v\n", cfg.Roots)
	fmt.Printf("  hub:     %s\n", hubLabel(cfg))
	fmt.Println()
	fmt.Println(paint(dim, "next: poppy scan"))
	return nil
}

func runVersion(ctx context.Context, args []string) error {
	fmt.Printf("poppy %s\n", agent.Version)
	return nil
}
