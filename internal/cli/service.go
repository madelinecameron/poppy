package cli

import (
	"os"
	"path/filepath"
)

// serviceLabel is the reverse-DNS identifier for poppy's launchd/systemd unit.
const serviceLabel = "com.github.madelinecameron.poppy"

// servicePath resolves the poppy binary to an absolute path with symlinks
// followed, so the generated service invokes the real executable even when
// poppy was started through a symlink on PATH.
func servicePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}
