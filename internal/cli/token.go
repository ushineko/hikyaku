package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ushineko/hikyaku/internal/secret"
)

func tokenCmd(env *Env) *cobra.Command {
	c := &cobra.Command{Use: "token", Short: "Store the Slack tokens, or say which are stored"}
	c.AddCommand(&cobra.Command{
		Use:   "set <bot|app>",
		Short: "Store a token, read from stdin",
		Long: "Store the bot token (xoxb-) or the app-level token (xapp-). The token is read from stdin, never " +
			"from an argument, so it does not reach shell history; at a terminal it is not echoed.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			k, err := secret.ParseKind(args[0])
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			raw, err := readToken(env, k)
			if err != nil {
				return err
			}
			v, err := secret.Check(k, raw)
			if err != nil {
				return fail(ExitError, "%v", err)
			}
			s, err := env.settings()
			if err != nil {
				return err
			}
			if err := env.secrets(s).Set(k, v); err != nil {
				return fail(ExitError, "%v", err)
			}
			_, _ = fmt.Fprintf(env.Stdout, "%s token stored\n", k)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Say which tokens are stored, without showing them",
		RunE: func(_ *cobra.Command, _ []string) error {
			s, err := env.settings()
			if err != nil {
				return err
			}
			store := env.secrets(s)
			missing := 0
			for _, k := range secret.Kinds {
				_, err := store.Get(k)
				switch {
				case err == nil:
					_, _ = fmt.Fprintf(env.Stdout, "%s token: stored\n", k)
				case errors.Is(err, secret.ErrNotFound):
					missing++
					_, _ = fmt.Fprintf(env.Stdout, "%s token: not stored (hikyaku token set %s)\n", k, k)
				default:
					return fail(ExitError, "%v", err)
				}
			}
			if missing > 0 {
				return fail(ExitError, "")
			}
			return nil
		},
	})
	return c
}

func readToken(env *Env, k secret.Kind) (string, error) {
	if env.IsTerminal() {
		_, _ = fmt.Fprintf(env.Stderr, "Paste the %s token (%s...) and press Enter: ", k, k.Prefix())
		v, err := env.ReadSecret()
		_, _ = fmt.Fprintln(env.Stderr)
		if err != nil {
			return "", fail(ExitError, "read token: %v", err)
		}
		return v, nil
	}
	line, err := bufio.NewReader(env.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fail(ExitError, "no token on stdin")
	}
	return strings.TrimSpace(line), nil
}

func stdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func readSecret() (string, error) {
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", fmt.Errorf("read from terminal: %w", err)
	}
	return string(b), nil
}
