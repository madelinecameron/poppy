//go:build darwin

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/madelinecameron/poppy/internal/config"
)

// launchd gives agents a minimal PATH, so poppy would fail to find git or ssh.
// Prepend the usual Homebrew and local locations to the default.
const servicePATH = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
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

	path := launchAgentPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderPlist(exe, daemonLogPath())), 0o644); err != nil {
		return err
	}

	target := "gui/" + strconv.Itoa(os.Getuid())
	// Remove any previous instance so bootstrap does not fail with "already loaded".
	exec.Command("launchctl", "bootout", target+"/"+serviceLabel).Run()
	if out, err := exec.Command("launchctl", "bootstrap", target, path).CombinedOutput(); err != nil {
		// Older macOS releases only understand the legacy verbs.
		if out2, err2 := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)+string(out2)))
		}
	}

	fmt.Printf("%s poppy service installed\n", paint(green, "ok"))
	fmt.Printf("  plist: %s\n", path)
	fmt.Printf("  logs:  %s\n", daemonLogPath())
	fmt.Println(paint(dim, "starts at login and restarts on crash; remove with: poppy daemon --uninstall"))
	return nil
}

func uninstallService() error {
	path := launchAgentPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("no service installed at %s", path)
	}
	target := "gui/" + strconv.Itoa(os.Getuid())
	if out, err := exec.Command("launchctl", "bootout", target+"/"+serviceLabel).CombinedOutput(); err != nil {
		exec.Command("launchctl", "unload", "-w", path).Run()
		_ = out
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	fmt.Printf("%s poppy service removed\n", paint(green, "ok"))
	return nil
}

func renderPlist(exe, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
		<string>--foreground</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, xmlEscape(serviceLabel), xmlEscape(exe), xmlEscape(servicePATH), xmlEscape(logPath), xmlEscape(logPath))
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
