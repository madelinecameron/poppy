//go:build !darwin && !linux

package cli

import (
	"fmt"
	"runtime"
)

func installService() error {
	return unsupportedService()
}

func uninstallService() error {
	return unsupportedService()
}

func unsupportedService() error {
	return fmt.Errorf("service install is not supported on %s; run 'poppy daemon' to background manually", runtime.GOOS)
}
