/*
Package secret stores the two Slack tokens and keeps them out of everything
hikyaku prints.

The keyring (the Secret Service, over D-Bus) is the default store. A 0600
file is the alternative, used only when the settings name it; nothing falls
back to it on its own.
*/
package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/ushineko/hikyaku/internal/atomicfile"
)

// Kind names one of the two tokens.
type Kind string

const (
	// Bot is the bot token, xoxb-, used for the Web API.
	Bot Kind = "bot"
	// App is the app-level token, xapp-, used to open the Socket Mode connection.
	App Kind = "app"
)

// Kinds lists the tokens in the order they are reported.
var Kinds = []Kind{Bot, App} //nolint:gochecknoglobals // a fixed list

// Prefix is the prefix every token of the kind starts with.
func (k Kind) Prefix() string {
	if k == App {
		return "xapp-"
	}
	return "xoxb-"
}

// ParseKind turns "bot" or "app" into a Kind.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case Bot, App:
		return Kind(s), nil
	default:
		return "", fmt.Errorf("unknown token %q: want bot or app", s)
	}
}

// ErrNotFound is returned for a token that has not been stored.
var ErrNotFound = errors.New("token not stored")

// ErrBadPrefix is returned for a value that does not look like the token asked for.
var ErrBadPrefix = errors.New("token has the wrong prefix")

// Store keeps the tokens.
type Store interface {
	Get(k Kind) (string, error)
	Set(k Kind, value string) error
	Delete(k Kind) error
}

// Check trims a token as pasted and checks its prefix. The error never
// carries the value.
func Check(k Kind, value string) (string, error) {
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, k.Prefix()) || len(v) <= len(k.Prefix()) {
		return "", fmt.Errorf("%s token: %w: want %s", k, ErrBadPrefix, k.Prefix())
	}
	return v, nil
}

const service = "hikyaku"

// Keyring is the Secret Service store.
type Keyring struct{}

// Get reads a token from the keyring.
func (Keyring) Get(k Kind) (string, error) {
	v, err := keyring.Get(service, string(k))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("%s token: %w", k, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read %s token from the keyring: %w", k, err)
	}
	return v, nil
}

// Set writes a token to the keyring.
func (Keyring) Set(k Kind, value string) error {
	if err := keyring.Set(service, string(k), value); err != nil {
		return fmt.Errorf("write %s token to the keyring: %w", k, err)
	}
	return nil
}

// Delete removes a token from the keyring; a missing one is not an error.
func (Keyring) Delete(k Kind) error {
	err := keyring.Delete(service, string(k))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("remove %s token from the keyring: %w", k, err)
	}
	return nil
}

// File stores each token in its own 0600 file in Dir, which is 0700.
type File struct{ Dir string }

func (f File) path(k Kind) string { return filepath.Join(f.Dir, string(k)+".token") }

// Get reads a token file.
func (f File) Get(k Kind) (string, error) {
	b, err := os.ReadFile(f.path(k))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s token: %w", k, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read %s token file: %w", k, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Set writes a token file, replacing it whole.
func (f File) Set(k Kind, value string) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	if err := atomicfile.Write(f.path(k), []byte(value+"\n")); err != nil {
		return fmt.Errorf("%s token: %w", k, err)
	}
	return nil
}

// Delete removes a token file; a missing one is not an error.
func (f File) Delete(k Kind) error {
	err := os.Remove(f.path(k))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s token file: %w", k, err)
	}
	return nil
}
