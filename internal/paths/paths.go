/*
Package paths resolves where hikyaku keeps its settings, its state and its
socket, from the XDG base directories.
*/
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const app = "hikyaku"

// ConfigDir is $XDG_CONFIG_HOME/hikyaku, or ~/.config/hikyaku.
func ConfigDir() (string, error) {
	return xdg("XDG_CONFIG_HOME", ".config")
}

// StateDir is $XDG_STATE_HOME/hikyaku, or ~/.local/state/hikyaku.
func StateDir() (string, error) {
	return xdg("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// RuntimeDir is $XDG_RUNTIME_DIR/hikyaku. Without XDG_RUNTIME_DIR it is a
// directory in the system's temporary directory named for the user's ID, so
// two accounts on one machine never share a socket.
func RuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, app)
	}
	return filepath.Join(os.TempDir(), app+"-"+strconv.Itoa(os.Getuid()))
}

// SocketPath is where the service listens and the CLI connects.
func SocketPath() string { return filepath.Join(RuntimeDir(), app+".sock") }

func xdg(env, fallback string) (string, error) {
	if d := os.Getenv(env); d != "" {
		return filepath.Join(d, app), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, fallback, app), nil
}

// ErrUnsafeDir is returned for a directory that exists but is not the
// user's own, or that other accounts can enter.
var ErrUnsafeDir = errors.New("directory is not private to this user")

// PrivateDir creates dir with mode 0700 if it is missing, and refuses one
// that exists with another owner or with any group or other permission bits.
// The socket and the state file rely on the directory to keep other accounts
// out.
func PrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: %w: not a directory", dir, ErrUnsafeDir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s: %w: owned by uid %d", dir, ErrUnsafeDir, st.Uid)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: %w: mode %o", dir, ErrUnsafeDir, fi.Mode().Perm())
	}
	return nil
}
