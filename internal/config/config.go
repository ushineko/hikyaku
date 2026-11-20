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
