package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/madelinecameron/poppy/internal/model"
	"github.com/madelinecameron/poppy/internal/scan"
)

// installedTools reports every fleet tool present on this machine, not only the
// ones that run as daemons.
//
// This is the installed half of what `houseparty fleet` compares against the
// toolchain contract. Reporting only daemons meant a non-daemon tool -- most of
// them -- could never be anything but "missing" in that diff, however current it
// actually was.
//
// The contract itself supplies the list, so a tool added to workplace-protocols.yaml is
// picked up without touching poppy. Names are looked up beside poppy's own
// binary rather than in a hardcoded directory: the fleet installs every tool
// into the same place, so wherever this process was started from is the right
// place to look.
func installedTools(ctx context.Context, workplaceProtocolsPath string) []model.Daemon {
	entries := readContract(workplaceProtocolsPath)
	if len(entries) == 0 {
		// No contract to read: still report ourselves, which is strictly better
		// than publishing nothing and reading as "poppy not installed".
		return []model.Daemon{{Name: "poppy", Version: Version(), Role: "agent"}}
	}

	dir := binDir()
	out := make([]model.Daemon, 0, len(entries))
	for _, e := range entries {
		path := filepath.Join(dir, e.Name)
		if !executable(path) {
			continue // not installed here; the contract diff will call it missing
		}
		d := model.Daemon{Name: e.Name, Role: e.Role}
		if e.Name == "poppy" {
			d.Version = Version() // no point shelling out to ourselves
		} else {
			d.Version = probeVersion(ctx, path)
		}
		out = append(out, d)
	}
	return out
}

// contractTool is the subset of a workplace-protocols.yaml toolchain entry we need.
type contractTool struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"`
}

// readContract pulls the tool list out of workplace-protocols.yaml. Every failure is silent
// and returns nothing: a machine with no contract on disk must still publish a
// scan, and a parse error here should never be able to stop one.
func readContract(path string) []contractTool {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(scan.Expand(path))
	if err != nil {
		return nil
	}
	var doc struct {
		Toolchain struct {
			Tools []contractTool `yaml:"tools"`
		} `yaml:"toolchain"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return doc.Toolchain.Tools
}

// binDir is the directory poppy itself was started from, which is where the
// fleet installs every other tool too.
func binDir() string {
	exe, err := os.Executable()
	if err != nil {
		return scan.Expand("~/.local/bin")
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func executable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// versionToken matches the first vX.Y.Z-ish token in a tool's output.
var versionToken = regexp.MustCompile(`v?\d+\.\d+\.\d+[0-9A-Za-z.+-]*`)

// probeVersion asks a tool what version it is. The fleet has no single
// convention -- poppy answers `version`, houseparty answers `--version`, and
// toolbox answers neither -- so both are tried and a tool that reports nothing
// is recorded as installed with an unknown version rather than omitted. Missing
// and present-but-unversioned are different facts and the contract diff should
// be able to tell them apart.
func probeVersion(ctx context.Context, path string) string {
	for _, arg := range []string{"version", "--version"} {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		out, err := exec.CommandContext(cctx, path, arg).CombinedOutput()
		cancel()
		if err != nil && len(out) == 0 {
			continue
		}
		// A usage dump means the flag was not understood; keep looking.
		text := string(out)
		if strings.Contains(text, "unknown command") || strings.Contains(text, "Usage of") {
			continue
		}
		if v := versionToken.FindString(firstLine(text)); v != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
