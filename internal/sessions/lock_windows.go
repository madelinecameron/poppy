//go:build windows

package sessions

import (
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx locks a byte range rather than the file; one byte at offset zero
// is enough, since every locker asks for the same range.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
