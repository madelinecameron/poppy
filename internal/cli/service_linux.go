//go:build linux

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/madelinecameron/poppy/internal/config"
)

func systemdUnitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", "poppy.service")
}

func installService() error {
	exe, err := servicePath()
	if err != nil {
		return err
	}
	if pid, ok := runningDaemon(); ok {
		fmt.Printf("%s a manually started daemon (pid %d) is running; stopping it\n", paint(yellow, "note"), pid)
		if err := stopDaemon(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(config.DataDir(), 0o755); err != nil {
		return err
	}

	path := systemdUnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderUnit(exe)), 0o644); err != nil {
		return err
	}

	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", "poppy.service"); err != nil {
		return err
	}
	// Lingering lets the user service start at boot without an interactive login.
	if out, err := exec.Command("loginctl", "enable-linger").CombinedOutput(); err != nil {
		fmt.Printf("%s could not enable linger; service starts at login rather than boot: %s\n",
			paint(yellow, "note"), strings.TrimSpace(string(out)))
	}

	fmt.Printf("%s poppy service installed\n", paint(green, "ok"))
	fmt.Printf("  unit: %s\n", path)
	fmt.Println(paint(dim, "status: systemctl --user status poppy · remove: poppy daemon --uninstall"))
	return nil
}

func uninstallService() error {
	path := systemdUnitPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("no service installed at %s", path)
	}
	systemctl("disable", "--now", "poppy.service")
	if err := os.Remove(path); err != nil {
		return err
	}
	systemctl("daemon-reload")
	fmt.Printf("%s poppy service removed\n", paint(green, "ok"))
	return nil
}

func systemctl(args ...string) error {
	full := append([]string{"--user"}, args...)
	if out, err := exec.Command("systemctl", full...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func renderUnit(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=poppy fleet-wide git awareness agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon --foreground
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
}
