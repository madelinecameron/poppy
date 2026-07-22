package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
	foreground := fs.Bool("foreground", false, "run the loop in this process instead of detaching")
	stop := fs.Bool("stop", false, "stop the running daemon")
	status := fs.Bool("status", false, "report whether the daemon is running")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch {
	case *stop:
		return stopDaemon()
	case *status:
		return daemonStatus()
	case *foreground:
		return runAgentLoop(ctx, *interval)
	default:
		return startDaemon(*interval)
	}
}

// startDaemon launches a detached background copy of poppy running the agent
// loop, then returns so the invoking shell is free. The child re-executes with
// --foreground and inherits a log file in place of the terminal.
func startDaemon(interval time.Duration) error {
	if pid, ok := runningDaemon(); ok {
		return fmt.Errorf("daemon already running (pid %d)", pid)
	}
	if err := os.MkdirAll(config.DataDir(), 0o755); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(daemonLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()

	childArgs := []string{"daemon", "--foreground"}
	if interval > 0 {
		childArgs = append(childArgs, "--interval", interval.String())
	}
	cmd := exec.Command(exe, childArgs...)
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(daemonPidPath(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s poppy daemon started (pid %d)\n", paint(green, "ok"), cmd.Process.Pid)
	fmt.Printf("  logs: %s\n", daemonLogPath())
	fmt.Println(paint(dim, "stop with: poppy daemon --stop"))
	return cmd.Process.Release()
}

// runAgentLoop runs the scan loop in the current process until interrupted.
func runAgentLoop(ctx context.Context, interval time.Duration) error {
	cfg, store, err := setup(ctx)
	if err != nil {
		return err
	}
	if interval > 0 {
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

func stopDaemon() error {
	pid, ok := runningDaemon()
	if !ok {
		os.Remove(daemonPidPath())
		return errors.New("no daemon running")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	os.Remove(daemonPidPath())
	fmt.Printf("%s stopped poppy daemon (pid %d)\n", paint(green, "ok"), pid)
	return nil
}

func daemonStatus() error {
	if pid, ok := runningDaemon(); ok {
		fmt.Printf("%s running (pid %d)\n", paint(green, "poppy daemon"), pid)
		return nil
	}
	fmt.Printf("%s not running\n", paint(yellow, "poppy daemon"))
	return nil
}

// runningDaemon reports the pid recorded in the pidfile when that process is
// still alive, so a stale pidfile left by a crashed daemon reads as stopped.
func runningDaemon() (int, bool) {
	data, err := os.ReadFile(daemonPidPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if syscall.Kill(pid, 0) != nil {
		return 0, false
	}
	return pid, true
}

func daemonPidPath() string { return filepath.Join(config.DataDir(), "daemon.pid") }
func daemonLogPath() string { return filepath.Join(config.DataDir(), "daemon.log") }

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
