package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/sessions"
)

// hookDeadline bounds one whole hook run. The harness waits on it before the
// user's prompt goes anywhere.
const hookDeadline = 5 * time.Second

// maxHookPayload bounds what is read from a hook's stdin.
const maxHookPayload = 1 << 20

// maxHookLog is the size at which the hook's error log starts over.
const maxHookLog = 1 << 20

// runSessionHook records one harness hook event. It never fails and never
// writes to stdout or stderr: Claude Code puts a hook's stdout into the
// model's context, and a hook that exits non-zero gets in the user's way. A
// failure goes to a log file in poppy's state directory instead.
func runSessionHook(ctx context.Context, args []string) error {
	if err := sessionHook(ctx, args, os.Stdin); err != nil {
		logHookError(args, err)
	}
	return nil
}

func sessionHook(ctx context.Context, args []string, stdin io.Reader) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, hookDeadline)
	defer cancel()

	if len(args) != 1 {
		return errors.New("usage: poppy session-hook <claude|codex>")
	}
	data, err := readHookInput(ctx, stdin)
	if err != nil {
		return err
	}
	payload, err := sessions.ParsePayload(data)
	if err != nil {
		return err
	}

	// A broken configuration file still leaves a session worth recording; it
	// only costs the mailbox lookup.
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		cfg = config.Defaults()
	}

	h := sessions.Hook{Harness: args[0], Dir: sessions.Dir()}
	if url := cfg.MailboxURL(); url != "" {
		h.Address = func(ctx context.Context, sessionID string) (string, error) {
			token := sessions.Token(os.Getenv)
			if token == "" {
				return "", errors.New("no MAILBOX_TOKEN in the environment or " + sessions.SecretsPath(os.Getenv))
			}
			return sessions.Whoami(ctx, nil, url, token, sessionID)
		}
	}
	return errors.Join(cfgErr, h.Handle(ctx, payload))
}

// readHookInput reads stdin within the hook's deadline, so a harness that
// never closes it cannot hang the prompt.
func readHookInput(ctx context.Context, stdin io.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(stdin, maxHookPayload))
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		return nil, errors.New("timed out reading the hook payload from stdin")
	}
}

func hookLogPath() string { return filepath.Join(config.DataDir(), "session-hook.log") }

func logHookError(args []string, hookErr error) {
	path := hookLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > maxHookLog {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	harness := ""
	if len(args) > 0 {
		harness = args[0]
	}
	slog.New(slog.NewTextHandler(f, nil)).Error("session hook", "harness", harness, "err", hookErr)
}

func runSessions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	repo := fs.String("repo", "", "only sessions that worked in this repository (name or id)")
	branch := fs.String("branch", "", "only sessions that worked on this branch")
	machine := fs.String("machine", "", "only sessions on this machine")
	active := fs.Bool("active", false, "only sessions seen recently and not ended")
	within := fs.Duration("within", 30*time.Minute, "how recently an --active session was seen")
	noPull := fs.Bool("no-pull", false, "skip fetching the latest fleet state")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, store, err := setupRead()
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

	filter := model.SessionFilter{
		Machine: *machine, Repo: *repo, Branch: *branch, Now: time.Now(),
	}
	if *active {
		filter.ActiveWithin = *within
	}
	list := fleet.Sessions(filter)
	if *asJSON {
		if list == nil {
			list = []model.Session{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	printSessions(os.Stdout, list, filter.Now, *within)
	return nil
}

// printSessions renders sessions one block each: who and where on the first
// line, then how to reach it and every repository and branch it was in.
func printSessions(w io.Writer, list []model.Session, now time.Time, within time.Duration) {
	if len(list) == 0 {
		fmt.Fprintln(w, "No sessions match.")
		return
	}
	activeCount := 0
	for _, s := range list {
		if s.Active(now, within) {
			activeCount++
		}
	}
	fmt.Fprintf(w, "%s  %s · %s\n", paint(bold, "SESSIONS"),
		plural(len(list), "session"), paint(green, fmt.Sprintf("%d active", activeCount)))

	for _, s := range list {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s %s\n", paint(bold+cyan, s.Machine+":"+s.Harness), paint(dim, s.SessionID))

		var details table
		seen := relTime(s.LastSeen, now)
		switch {
		case s.Ended != nil:
			seen = paint(dim, "ended "+relTime(*s.Ended, now))
		case s.Active(now, within):
			seen = paint(green, seen)
		default:
			seen = paint(yellow, seen)
		}
		details.add("seen", seen+paint(dim, " · started "+relTime(s.FirstSeen, now)))
		mailbox := s.Mailbox
		if mailbox == "" {
			mailbox = paint(dim, "(none)")
		}
		details.add("mailbox", mailbox)
		if s.TranscriptPath != "" {
			details.add("transcript", paint(dim, s.TranscriptPath))
		}
		details.render(w, "  ")

		if len(s.Repos) == 0 {
			continue
		}
		var repos table
		repos.setHeader("REPO", "BRANCH", "LAST", "SINCE", "PATH")
		for _, r := range s.Repos {
			branch := r.Branch
			if branch == "" {
				branch = "(detached)"
			}
			repos.add(r.Name, branch, relTime(r.LastSeen, now),
				paint(dim, relTime(r.FirstSeen, now)), paint(dim, r.Path))
		}
		repos.render(w, "  ")
	}
}
