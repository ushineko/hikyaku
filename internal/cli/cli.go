/*
Package cli is the hikyaku command: the service (`serve`) and the commands a
coding agent calls (`send`, `ask`, `inbox`), plus `token` and `status`.
*/
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"time"

	"github.com/spf13/cobra"

	"github.com/ushineko/hikyaku/internal/answerer"
	"github.com/ushineko/hikyaku/internal/buildinfo"
	"github.com/ushineko/hikyaku/internal/config"
	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/paths"
	"github.com/ushineko/hikyaku/internal/secret"
)

// Exit codes. 124 for a timeout is what timeout(1) uses, so a script that
// already knows that convention knows this one.
const (
	ExitOK        = 0
	ExitError     = 1
	ExitUsage     = 2
	ExitNoService = 3
	ExitTimeout   = 124
)

// ExitStatus carries the exit code a command wants.
type ExitStatus struct {
	Code int
	Msg  string
}

func (e *ExitStatus) Error() string { return e.Msg }

func fail(code int, format string, args ...any) error {
	return &ExitStatus{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Env is everything a command reads from outside itself, so tests can give
// it their own.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// IsTerminal reports whether Stdin is a terminal.
	IsTerminal func() bool
	// ReadSecret reads a line from the terminal without echoing it.
	ReadSecret func() (string, error)

	SocketPath   string
	SettingsPath string
	StatePath    string
	// Secrets overrides the store the settings name, for tests.
	Secrets secret.Store
	// APIURL overrides Slack's API address, for tests.
	APIURL string
	// Origin says where a message comes from when --from is not given.
	Origin func(context.Context) string
	// Runner overrides the answerer's runs (real Claude Code), for tests.
	Runner answerer.Runner
	// PlainRunner overrides the runs for people other than an admin, which
	// read nothing (spec 009); with Runner set and this nil, Runner is used.
	PlainRunner answerer.Runner
	// Now overrides the service's clock, for tests.
	Now func() time.Time
}

// DefaultEnv is the real process's environment.
func DefaultEnv() (*Env, error) {
	settings, err := config.Path()
	if err != nil {
		return nil, err
	}
	state, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return &Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		IsTerminal: stdinIsTerminal, ReadSecret: readSecret,
		SocketPath: paths.SocketPath(), SettingsPath: settings,
		StatePath: filepath.Join(state, "messages.json"),
		Origin:    processOrigin,
	}, nil
}

func (e *Env) settings() (config.Settings, error) {
	s, err := config.Load(e.SettingsPath)
	if err != nil {
		return s, fail(ExitError, "%v", err)
	}
	return s, nil
}

func (e *Env) secrets(s config.Settings) secret.Store {
	if e.Secrets != nil {
		return e.Secrets
	}
	return s.Secrets(filepath.Dir(e.SettingsPath))
}

// Root builds the command tree.
func Root(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:           "hikyaku",
		Short:         "Carry messages between coding agents and Slack",
		Version:       buildinfo.Version(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	root.AddCommand(serveCmd(env), sendCmd(env), askCmd(env), inboxCmd(env), listenCmd(env), answererCmd(env), personaCmd(env), usersCmd(env), tokenCmd(env), statusCmd(env))
	return root
}

// Execute runs the command line and returns the process's exit code.
// Cancelling ctx stops a running `serve`.
func Execute(ctx context.Context, env *Env, args []string) int {
	root := Root(env)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var ee *ExitStatus
	if errors.As(err, &ee) {
		if ee.Msg != "" {
			_, _ = fmt.Fprintln(env.Stderr, "hikyaku:", ee.Msg)
		}
		return ee.Code
	}
	_, _ = fmt.Fprintln(env.Stderr, "hikyaku:", err)
	return ExitUsage
}

// callErr turns a failed call or an error response into an exit.
func callErr(err error, resp ipc.Response) error {
	if errors.Is(err, ipc.ErrNoService) {
		return fail(ExitNoService, "%v", err)
	}
	if err != nil {
		return fail(ExitError, "%v", err)
	}
	if resp.Code == ipc.CodeTimeout {
		return fail(ExitTimeout, "%s", resp.Error)
	}
	return fail(ExitError, "%s", resp.Error)
}
