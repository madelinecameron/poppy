// Package config loads poppy's machine-local configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// StaleAfterSeconds is when a machine's heartbeat is considered stale.
	StaleAfterSeconds int `json:"staleAfterSeconds,omitempty"`
}

// Defaults returns the configuration used when none has been written.
func Defaults() Config {
	return Config{
		Machine:             hostname(),
		Roots:               []string{"~/code"},
		ScanIntervalSeconds: 300,
		IncludeWorktrees:    true,
		MaxDepth:            8,
		StaleAfterSeconds:   3600,
	}
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
