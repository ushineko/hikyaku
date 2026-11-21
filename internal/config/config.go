/*
Package config holds hikyaku's settings: whom it talks to, who may answer,
where the tokens are kept and how long a message is remembered.
*/
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ushineko/hikyaku/internal/paths"
	"github.com/ushineko/hikyaku/internal/secret"
)

// TokenStore names where the tokens are kept.
type TokenStore string

const (
	// StoreKeyring is the Secret Service, the default.
	StoreKeyring TokenStore = "keyring"
	// StoreFile is 0600 files in the settings directory, only when chosen.
	StoreFile TokenStore = "file"
)

// DefaultRetention is how long a message and its replies are remembered.
const DefaultRetention = 7 * 24 * time.Hour

// Settings is the settings file, settings.yaml.
type Settings struct {
	// Owner is the Slack user ID whose DM with the bot is the default
	// destination. Spec 002's pairing writes it; until then it is set by hand.
	Owner string `yaml:"owner,omitempty"`
	// AllowedRepliers are the Slack user IDs whose replies reach an agent.
	// Empty means the owner alone.
	AllowedRepliers []string `yaml:"allowed_repliers,omitempty"`
	// TokenStore is keyring or file; empty is keyring.
	TokenStore TokenStore `yaml:"token_store,omitempty"`
	// Retention is how long a message is remembered, as a Go duration; empty
	// is seven days.
	Retention string `yaml:"retention,omitempty"`
	// Persona is the persona file (spec 008), absolute or relative to the
	// settings' directory; empty is SOUL.md there.
	Persona string `yaml:"persona,omitempty"`
	// Answerer configures the answerer (spec 007). Whether it is on is not
	// a setting: it is switched with `hikyaku answerer on|off`.
	Answerer Answerer `yaml:"answerer,omitempty"`
}

// Answerer is how the answerer's runs are started. Every field has a
// default; an empty section is the defaults.
type Answerer struct {
	// Claude is the Claude Code command, by name or path.
	Claude string `yaml:"claude,omitempty"`
	// Model is passed as --model when set; empty is Claude Code's default.
	Model string `yaml:"model,omitempty"`
	// AllowedTools replaces the default read-only allow-list. Everything
	// else is denied.
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
	// MCPConfig is an MCP configuration file for the runs; no other MCP
	// server is loaded.
	MCPConfig string `yaml:"mcp_config,omitempty"`
	// RunBudgetUSD and DayBudgetUSD cap the cost of one run and of a day.
	RunBudgetUSD float64 `yaml:"run_budget_usd,omitempty"`
	DayBudgetUSD float64 `yaml:"day_budget_usd,omitempty"`
	// RunTimeout is how long a run may take, as a Go duration.
	RunTimeout string `yaml:"run_timeout,omitempty"`
}

// Answerer defaults (spec 007, D4 and D5).
const (
	DefaultRunBudgetUSD = 0.50
	DefaultDayBudgetUSD = 5.00
	DefaultRunTimeout   = 5 * time.Minute
)

// DefaultAllowedTools is the read-only allow-list (spec 007, D4): read
// subcommands of glab and gh, Jira's read tools, and reading the
// repositories under ~/git. No tool that writes, and none that reaches an
// arbitrary address.
func DefaultAllowedTools() []string {
	tools := []string{
		"Read(~/git/**)", "Grep(~/git/**)", "Glob(~/git/**)",
		"Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)",
	}
	for _, c := range []string{
		"glab mr list", "glab mr view", "glab ci status", "glab ci list", "glab ci get",
		"glab release list", "glab release view", "glab issue list", "glab issue view",
		"gh pr list", "gh pr view", "gh pr status", "gh pr checks", "gh run list", "gh run view",
		"gh release list", "gh release view", "gh issue list", "gh issue view",
	} {
		tools = append(tools, "Bash("+c+":*)")
	}
	for _, t := range []string{"jira_get_issue", "jira_search", "jira_get_project_issues", "jira_get_sprint_issues"} {
		tools = append(tools, "mcp__atlassian__"+t)
	}
	return tools
}

// Tools is the allow-list in effect.
func (a Answerer) Tools() []string {
	if len(a.AllowedTools) > 0 {
		return slices.Clone(a.AllowedTools)
	}
	return DefaultAllowedTools()
}

// Command is the Claude Code command in effect.
func (a Answerer) Command() string {
	if a.Claude != "" {
		return a.Claude
	}
	return "claude"
}

// RunBudget is the cost cap of one run.
func (a Answerer) RunBudget() float64 {
	if a.RunBudgetUSD > 0 {
		return a.RunBudgetUSD
	}
	return DefaultRunBudgetUSD
}

// DayBudget is the cost cap of a day.
func (a Answerer) DayBudget() float64 {
	if a.DayBudgetUSD > 0 {
		return a.DayBudgetUSD
	}
	return DefaultDayBudgetUSD
}

// Timeout is how long one run may take.
func (a Answerer) Timeout() time.Duration {
	if d, err := time.ParseDuration(a.RunTimeout); err == nil && d > 0 {
		return d
	}
	return DefaultRunTimeout
}

// Path is the settings file's location.
func Path() (string, error) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.yaml"), nil
}

// Load reads the settings file; a missing file is empty settings.
func Load(path string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read settings: %w", err)
	}
	if err := yaml.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parse settings %s: %w", path, err)
	}
	if err := s.validate(); err != nil {
		return s, fmt.Errorf("settings %s: %w", path, err)
	}
	return s, nil
}

func (s Settings) validate() error {
	switch s.TokenStore {
	case "", StoreKeyring, StoreFile:
	default:
		return fmt.Errorf("token_store %q: want keyring or file", s.TokenStore)
	}
	if s.Retention != "" {
		if d, err := time.ParseDuration(s.Retention); err != nil || d <= 0 {
			return fmt.Errorf("retention %q: want a positive duration such as 168h", s.Retention)
		}
	}
	if s.Answerer.RunTimeout != "" {
		if d, err := time.ParseDuration(s.Answerer.RunTimeout); err != nil || d <= 0 {
			return fmt.Errorf("answerer.run_timeout %q: want a positive duration such as 5m", s.Answerer.RunTimeout)
		}
	}
	if s.Answerer.RunBudgetUSD < 0 || s.Answerer.DayBudgetUSD < 0 {
		return errors.New("answerer budgets must not be negative")
	}
	return nil
}

// RetentionPeriod is Retention as a duration.
func (s Settings) RetentionPeriod() time.Duration {
	if d, err := time.ParseDuration(s.Retention); err == nil && d > 0 {
		return d
	}
	return DefaultRetention
}

// Repliers is the allow-list in effect: AllowedRepliers, or the owner when
// the list is empty. Empty when neither is set, and then no one can reply.
func (s Settings) Repliers() []string {
	if len(s.AllowedRepliers) > 0 {
		return slices.Clone(s.AllowedRepliers)
	}
	if s.Owner != "" {
		return []string{s.Owner}
	}
	return nil
}

// Secrets opens the token store the settings name. dir is the settings
// directory, where the file store keeps its files.
func (s Settings) Secrets(dir string) secret.Store {
	if s.TokenStore == StoreFile {
		return secret.File{Dir: dir}
	}
	return secret.Keyring{}
}
