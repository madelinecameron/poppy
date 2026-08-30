// Package config loads poppy's machine-local configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/madelinecameron/poppy/internal/mirror"
	"github.com/madelinecameron/poppy/internal/secretscan"
	"github.com/madelinecameron/poppy/internal/snapshot"
)

// Config is the machine-local configuration.
type Config struct {
	// Machine names this host in the fleet. Defaults to the hostname.
	Machine string `json:"machine"`
	// Roots are directories scanned for repositories.
	Roots []string `json:"roots"`
	// HubRemote is the git URL of the shared hub repository. When empty poppy
	// runs local-only: state is still recorded and queryable on this machine,
	// but nothing is shared with the fleet.
	HubRemote string `json:"hubRemote"`
	// ScanIntervalSeconds is how often the daemon rescans.
	ScanIntervalSeconds int `json:"scanIntervalSeconds"`
	// SkipDirs are additional directory names never descended into.
	SkipDirs []string `json:"skipDirs,omitempty"`
	// MaxDepth bounds how deep beneath a root a repository may be found.
	MaxDepth int `json:"maxDepth,omitempty"`
	// IncludeWorktrees reports linked worktrees as their own entries.
	IncludeWorktrees bool `json:"includeWorktrees"`
	// WorkplaceProtocolsRoles are the protocol roles this machine plays (e.g. "closet",
	// "hub-host", "registry-host"), published in the machine's manifest so the
	// fleet is discoverable without a coordinator.
	WorkplaceProtocolsRoles []string `json:"workplaceProtocolsRoles,omitempty"`
	// StaleAfterSeconds is when a machine's heartbeat is considered stale.
	StaleAfterSeconds int `json:"staleAfterSeconds,omitempty"`
	// Snapshot controls the local time machine.
	// WorkplaceProtocolsPath is the toolchain contract this machine reports against. The
	// tool list in it decides which installed binaries poppy publishes, so a
	// tool added to workplace-protocols.yaml is picked up without changing poppy.
	WorkplaceProtocolsPath string `json:"workplaceProtocolsPath,omitempty"`

	Snapshot Snapshot `json:"snapshot"`
	// Secrets controls credential scanning.
	Secrets Secrets `json:"secrets"`
}

// Secrets configures the credential scanner. Every bound is explicit: a scan
// that quietly stopped early would make a clean report meaningless.
type Secrets struct {
	// Enabled turns scanning on as part of each cycle.
	Enabled bool `json:"enabled"`
	// Gitleaks additionally runs gitleaks over each working tree when it is on
	// PATH. Off by default so that two machines scanning the same repository
	// cannot disagree because of what happens to be installed on each.
	Gitleaks bool `json:"gitleaks,omitempty"`
	// IntervalSeconds is the longest a scan result is reused. Within it, a
	// repository whose git state has not changed is not re-read.
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
	// MaxFileBytes bounds the size of a file or object that is examined.
	MaxFileBytes int64 `json:"maxFileBytes,omitempty"`
	// MaxWorktreeFiles bounds how many working-tree files are read.
	MaxWorktreeFiles int `json:"maxWorktreeFiles,omitempty"`
	// MaxUnpushedCommits bounds the walk over commits no remote has. A
	// repository with no remote at all has no upper bound but this one.
	MaxUnpushedCommits int `json:"maxUnpushedCommits,omitempty"`
	// MaxBlobs bounds how many distinct objects from stashes and unpushed
	// commits are read.
	MaxBlobs int `json:"maxBlobs,omitempty"`
	// MaxFindings bounds how many findings one repository records.
	MaxFindings int `json:"maxFindings,omitempty"`
	// TimeoutSeconds bounds one repository's scan.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// ExcludeRepos are glob patterns matched against a repository path;
	// matching repositories are never scanned.
	ExcludeRepos []string `json:"excludeRepos,omitempty"`
}

// Snapshot configures capture of uncommitted work.
type Snapshot struct {
	// Enabled turns capture on. Snapshots are written to refs/poppy/* inside
	// each repository and never leave the machine on their own.
	Enabled bool `json:"enabled"`
	// MaxFileBytes bounds the size of an untracked file that will be captured,
	// keeping stray build output out of the object store.
	MaxFileBytes int64 `json:"maxFileBytes,omitempty"`
	// KeepAllHours retains every snapshot taken within this window.
	KeepAllHours int `json:"keepAllHours,omitempty"`
	// KeepDailyHours retains one snapshot per day within this window.
	KeepDailyHours int `json:"keepDailyHours,omitempty"`
	// ExcludeRepos are glob patterns matched against a repository path;
	// matching repositories are never captured.
	ExcludeRepos []string `json:"excludeRepos,omitempty"`
	// Mirror sends snapshots off the machine, which backs them up and lets
	// another machine teleport the work with grab.
	Mirror Mirror `json:"mirror"`
}

// Mirror configures where snapshots are backed up.
type Mirror struct {
	// Enabled turns off-machine sync on.
	Enabled bool `json:"enabled"`
	// Host is the SSH target of the closet server. Empty means Dir is a path
	// on the local machine, for a single-box setup.
	Host string `json:"host,omitempty"`
	// Dir holds the bare per-repository mirrors, on Host when set.
	Dir string `json:"dir,omitempty"`
}

// Defaults returns the configuration used when none has been written.
func Defaults() Config {
	return Config{
		Machine:                hostname(),
		Roots:                  []string{"~/code"},
		ScanIntervalSeconds:    300,
		IncludeWorktrees:       true,
		MaxDepth:               8,
		StaleAfterSeconds:      3600,
		WorkplaceProtocolsPath: "~/.dev-config/workplace-protocols.yaml",
		Snapshot: Snapshot{
			Enabled:        true,
			MaxFileBytes:   10 << 20,
			KeepAllHours:   7 * 24,
			KeepDailyHours: 90 * 24,
		},
		Secrets: Secrets{
			Enabled:            true,
			IntervalSeconds:    3600,
			MaxFileBytes:       secretscan.DefaultMaxFileBytes,
			MaxWorktreeFiles:   secretscan.DefaultMaxWorktreeFiles,
			MaxUnpushedCommits: secretscan.DefaultMaxUnpushedCommits,
			MaxBlobs:           secretscan.DefaultMaxBlobs,
			MaxFindings:        secretscan.DefaultMaxFindings,
			TimeoutSeconds:     int(secretscan.DefaultTimeout.Seconds()),
		},
	}
}

// ExcludesRepo reports whether a repository path is excluded from scanning.
func (s Secrets) ExcludesRepo(path string) bool {
	return matchesAny(s.ExcludeRepos, path)
}

// ScanOptions converts the stored settings into scanner bounds.
func (s Secrets) ScanOptions() secretscan.Options {
	return secretscan.Options{
		MaxFileBytes:       s.MaxFileBytes,
		MaxWorktreeFiles:   s.MaxWorktreeFiles,
		MaxUnpushedCommits: s.MaxUnpushedCommits,
		MaxBlobs:           s.MaxBlobs,
		MaxFindings:        s.MaxFindings,
		Timeout:            time.Duration(s.TimeoutSeconds) * time.Second,
		Gitleaks:           s.Gitleaks,
	}
}

// Interval is how long a scan result may be reused for an unchanged repository.
func (s Secrets) Interval() time.Duration {
	return time.Duration(s.IntervalSeconds) * time.Second
}

// ExcludesRepo reports whether a repository path is excluded from capture.
func (s Snapshot) ExcludesRepo(path string) bool {
	return matchesAny(s.ExcludeRepos, path)
}

// matchesAny reports whether a repository path matches a glob pattern either
// whole or by its final component, so both "/home/mads/code/client-*" and
// "client-*" exclude what a person means by them.
func matchesAny(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if ok, err := filepath.Match(pattern, path); err == nil && ok {
			return true
		}
		if ok, err := filepath.Match(pattern, filepath.Base(path)); err == nil && ok {
			return true
		}
	}
	return false
}

// MirrorConfig converts the stored settings into a mirror destination.
func (m Mirror) MirrorConfig() mirror.Config {
	if !m.Enabled {
		return mirror.Config{}
	}
	return mirror.Config{Host: m.Host, Dir: m.Dir}
}

// Policy converts the configured windows into a retention policy.
func (s Snapshot) Policy() snapshot.Policy {
	p := snapshot.DefaultPolicy()
	if s.KeepAllHours > 0 {
		p.KeepAll = time.Duration(s.KeepAllHours) * time.Hour
	}
	if s.KeepDailyHours > 0 {
		p.KeepDaily = time.Duration(s.KeepDailyHours) * time.Hour
	}
	return p
}

// Path returns the configuration file location.
func Path() string {
	if p := os.Getenv("POPPY_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(configHome(), "poppy", "config.json")
}

// DataDir returns the directory holding the local hub clone and agent state.
func DataDir() string {
	if p := os.Getenv("POPPY_DATA_DIR"); p != "" {
		return p
	}
	if p := os.Getenv("XDG_DATA_HOME"); p != "" {
		return filepath.Join(p, "poppy")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "poppy")
	}
	return filepath.Join(home, ".local", "share", "poppy")
}

func configHome() string {
	if p := os.Getenv("XDG_CONFIG_HOME"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, ".config")
}

// Load reads the configuration, falling back to defaults when absent.
func Load() (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", Path(), err)
	}
	return cfg.withDefaults(), nil
}

func (c Config) withDefaults() Config {
	d := Defaults()
	if c.Machine == "" {
		c.Machine = d.Machine
	}
	if len(c.Roots) == 0 {
		c.Roots = d.Roots
	}
	if c.ScanIntervalSeconds <= 0 {
		c.ScanIntervalSeconds = d.ScanIntervalSeconds
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = d.MaxDepth
	}
	if c.StaleAfterSeconds <= 0 {
		c.StaleAfterSeconds = d.StaleAfterSeconds
	}
	if c.Snapshot.MaxFileBytes <= 0 {
		c.Snapshot.MaxFileBytes = d.Snapshot.MaxFileBytes
	}
	if c.Snapshot.KeepAllHours <= 0 {
		c.Snapshot.KeepAllHours = d.Snapshot.KeepAllHours
	}
	if c.Snapshot.KeepDailyHours <= 0 {
		c.Snapshot.KeepDailyHours = d.Snapshot.KeepDailyHours
	}
	if c.Secrets.IntervalSeconds <= 0 {
		c.Secrets.IntervalSeconds = d.Secrets.IntervalSeconds
	}
	if c.Secrets.MaxFileBytes <= 0 {
		c.Secrets.MaxFileBytes = d.Secrets.MaxFileBytes
	}
	if c.Secrets.MaxWorktreeFiles <= 0 {
		c.Secrets.MaxWorktreeFiles = d.Secrets.MaxWorktreeFiles
	}
	if c.Secrets.MaxUnpushedCommits <= 0 {
		c.Secrets.MaxUnpushedCommits = d.Secrets.MaxUnpushedCommits
	}
	if c.Secrets.MaxBlobs <= 0 {
		c.Secrets.MaxBlobs = d.Secrets.MaxBlobs
	}
	if c.Secrets.MaxFindings <= 0 {
		c.Secrets.MaxFindings = d.Secrets.MaxFindings
	}
	if c.Secrets.TimeoutSeconds <= 0 {
		c.Secrets.TimeoutSeconds = d.Secrets.TimeoutSeconds
	}
	return c
}

// Save writes the configuration, creating parent directories.
func Save(c Config) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Exists reports whether a configuration file has been written.
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	// macOS reports transient names such as "host.local" or "host.lan".
	for _, suffix := range []string{".local", ".lan", ".home"} {
		h = strings.TrimSuffix(h, suffix)
	}
	return h
}
