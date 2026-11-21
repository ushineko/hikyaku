package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ushineko/hikyaku/internal/ipc"
)

// usersCmd manages who may talk to the bot (spec 009). It works only over
// the local socket: nothing typed in Slack changes access.
func usersCmd(env *Env) *cobra.Command {
	c := &cobra.Command{
		Use:   "users",
		Short: "List or change who may talk to the bot besides admins",
		Long: "Admins are the settings' `admins` (the owner by default). Users may talk to the bot, by @-mention, " +
			"in a channel it is in, and get answers that read nothing; `--dm` also lets them DM it. Changes apply " +
			"at once. Nothing typed in Slack can change this list.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return usersCall(cmd, env, "", "", false) },
	}
	var dm bool
	add := &cobra.Command{
		Use: "add <user-id>", Short: "Let a user talk to the bot in channels", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return usersCall(cmd, env, "add", args[0], dm) },
	}
	add.Flags().BoolVar(&dm, "dm", false, "also let them DM the bot")
	remove := &cobra.Command{
		Use: "remove <user-id>", Short: "Take a user's access away", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return usersCall(cmd, env, "remove", args[0], false) },
	}
	dmCmd := &cobra.Command{
		Use: "dm <user-id> on|off", Short: "Grant or take away a user's DM access", Args: cobra.ExactArgs(2),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "on" && args[1] != "off" {
				return fail(ExitUsage, "want on or off, not %q", args[1])
			}
			return usersCall(cmd, env, "dm-"+args[1], args[0], false)
		},
	}
	c.AddCommand(add, remove, dmCmd)
	return c
}

func usersCall(cmd *cobra.Command, env *Env, op, user string, dm bool) error {
	resp, err := ipc.Call(cmd.Context(), env.SocketPath, ipc.Request{Op: ipc.OpUsers, Switch: op, User: user, DM: dm})
	if err != nil || !resp.OK {
		return callErr(err, resp)
	}
	for _, u := range resp.Users {
		role := "user"
		if u.Admin {
			role = "admin"
		}
		dmText := "channels only"
		if u.DM {
			dmText = "channels and DM"
		}
		_, _ = fmt.Fprintf(env.Stdout, "%s  %s, %s; $%.2f today\n", u.ID, role, dmText, u.Spent)
	}
	return nil
}
