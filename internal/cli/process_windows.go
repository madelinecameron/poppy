//go:build windows

package cli

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachedProcAttr starts the daemon outside the console and its process
// group, so closing the window or a Ctrl-C there does not reach it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}

// Windows has no SIGTERM to deliver to another process; the daemon is ended
// outright and does not get to log its own shutdown.
func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// os.FindProcess succeeds for any pid on Windows, so liveness is read from the
// process's exit code instead.
func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == uint32(windows.STATUS_PENDING)
}
