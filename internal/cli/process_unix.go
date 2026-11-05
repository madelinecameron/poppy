//go:build !windows

package cli

import "syscall"

// detachedProcAttr puts the daemon in its own session, so closing the
// terminal that started it does not take it down.
func detachedProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
