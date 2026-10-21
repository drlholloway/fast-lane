// Package config loads and saves Fast Lane's user configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EventKind identifies a type of GitHub activity Fast Lane can watch.
type EventKind string

const (
	KindPullRequest  EventKind = "pull_request"
	KindIssue        EventKind = "issue"
	KindDependabot   EventKind = "dependabot_alert"
	KindCodeScanning EventKind = "code_scanning_alert"
)

// AllKinds lists every supported event kind in display order.
var AllKinds = []EventKind{KindPullRequest, KindIssue, KindDependabot, KindCodeScanning}

// Short returns a compact label for the kind.
func (k EventKind) Short() string {
	switch k {
	case KindPullRequest:
		return "PR"
	case KindIssue:
		return "Issue"
	case KindDependabot:
		return "Dependabot"
	case KindCodeScanning:
		return "CodeScan"
	}
	return string(k)
}

// Duration is a time.Duration that marshals as a human-readable string ("2m").
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// Repo holds the per-repository watch settings.
type Repo struct {
	// FullName is "owner/name".
	FullName string `json:"full_name"`
	// LocalPath is an existing checkout to run Claude in. When empty the repo
	// is cloned under Config.WorkspaceDir.
	LocalPath string `json:"local_path,omitempty"`
	// Events lists which kinds of activity to watch.
	Events []EventKind `json:"events"`
	// AutoRun starts a Claude session for new events without asking.
	AutoRun bool `json:"auto_run"`
}

// Watches reports whether the repo watches the given kind.
func (r Repo) Watches(k EventKind) bool {
	for _, e := range r.Events {
		if e == k {
			return true
		}
	}
	return false
}

// Claude holds settings for launching Claude Code sessions.
type Claude struct {
	// Path is the claude executable.
	Path string `json:"path"`
	// Model is passed to --model when set.
	Model string `json:"model,omitempty"`
	// PermissionMode is passed to --permission-mode. Prompts that the mode
	// does not settle on its own are shown in the TUI.
	PermissionMode string `json:"permission_mode"`
	// AllowedTools are pre-approved and never prompt.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// MaxBudgetUSD caps the spend of a single session when greater than zero.
	MaxBudgetUSD float64 `json:"max_budget_usd,omitempty"`
	// ExtraArgs are appended to every claude invocation.
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// Config is the full user configuration.
type Config struct {
	PollInterval  Duration `json:"poll_interval"`
	WorkspaceDir  string   `json:"workspace_dir"`
	MaxConcurrent int      `json:"max_concurrent"`
	Claude        Claude   `json:"claude"`
	// Prompts overrides the default prompt template for an event kind.
	// Templates use Go text/template syntax; see the README for fields.
	Prompts map[EventKind]string `json:"prompts,omitempty"`
	Repos   []Repo               `json:"repos"`

	path string
}

// Default returns a configuration with sensible defaults.
func Default() *Config {
	return &Config{
		PollInterval:  Duration{2 * time.Minute},
		WorkspaceDir:  filepath.Join(dataHome(), "fast-lane", "workspaces"),
		MaxConcurrent: 2,
		Claude: Claude{
			Path:           "claude",
			PermissionMode: "manual",
			AllowedTools: []string{
				"Read", "Grep", "Glob",
				"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
				"Bash(gh pr view:*)", "Bash(gh pr diff:*)", "Bash(gh pr checks:*)",
				"Bash(gh issue view:*)", "Bash(gh api:*)",
			},
		},
	}
}

// DefaultPath returns the default location of the config file.
func DefaultPath() string {
	return filepath.Join(configHome(), "fast-lane", "config.json")
}

// Load reads the config at path, returning defaults when it does not exist.
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.WorkspaceDir = expandHome(cfg.WorkspaceDir)
	for i := range cfg.Repos {
		cfg.Repos[i].LocalPath = expandHome(cfg.Repos[i].LocalPath)
	}
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	if cfg.PollInterval.Duration < 30*time.Second {
		cfg.PollInterval.Duration = 30 * time.Second
	}
	return cfg, nil
}

// Path returns the file the config was loaded from.
func (c *Config) Path() string { return c.path }

// Save writes the config back to the file it was loaded from.
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("config has no path")
	}
	sort.Slice(c.Repos, func(i, j int) bool { return c.Repos[i].FullName < c.Repos[j].FullName })
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Repo returns the watch settings for fullName, or nil when it is not watched.
func (c *Config) Repo(fullName string) *Repo {
	for i := range c.Repos {
		if strings.EqualFold(c.Repos[i].FullName, fullName) {
			return &c.Repos[i]
		}
	}
	return nil
}

// Watch starts watching fullName with all event kinds enabled. It is a no-op
// when the repo is already watched.
func (c *Config) Watch(fullName string) *Repo {
	if r := c.Repo(fullName); r != nil {
		return r
	}
	c.Repos = append(c.Repos, Repo{FullName: fullName, Events: append([]EventKind(nil), AllKinds...)})
	return &c.Repos[len(c.Repos)-1]
}

// Unwatch stops watching fullName.
func (c *Config) Unwatch(fullName string) {
	for i := range c.Repos {
		if strings.EqualFold(c.Repos[i].FullName, fullName) {
			c.Repos = append(c.Repos[:i], c.Repos[i+1:]...)
			return
		}
	}
}

// WorkDir returns the directory Claude should run in for fullName.
func (c *Config) WorkDir(fullName string) string {
	if r := c.Repo(fullName); r != nil && r.LocalPath != "" {
		return r.LocalPath
	}
	return filepath.Join(c.WorkspaceDir, filepath.FromSlash(fullName))
}

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(home(), ".config")
}

func dataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(home(), ".local", "share")
}

// StateHome returns the base directory for runtime state.
func StateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "fast-lane")
	}
	return filepath.Join(home(), ".local", "state", "fast-lane")
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func expandHome(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home(), p[2:])
	}
	return p
}
