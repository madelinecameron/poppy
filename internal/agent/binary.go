package agent

import (
	"errors"
	"os"
)

// ErrBinaryReplaced ends Run when the executable the daemon started from has
// been replaced on disk. The service manager restarts the daemon on any exit
// (systemd Restart=always, launchd KeepAlive), so exiting is how a new
// version takes over. Fleet converge only swaps the file.
var ErrBinaryReplaced = errors.New("poppy binary replaced on disk")

// WatchExecutable returns a check that reports whether the file at this
// process's executable path is no longer the one it started from: a new
// file renamed over it, or the same file rewritten in place. A file that is
// missing or unreadable mid-swap reads as unchanged, so the next check, not
// this one, decides.
func WatchExecutable() (func() bool, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	started, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return func() bool { return replaced(path, started) }, nil
}

func replaced(path string, started os.FileInfo) bool {
	now, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !os.SameFile(started, now) || !now.ModTime().Equal(started.ModTime()) || now.Size() != started.Size()
}
