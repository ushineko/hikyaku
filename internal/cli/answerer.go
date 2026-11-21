package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ushineko/hikyaku/internal/ipc"
)

func answererCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:       "answerer [on|off|status]",
		Short:     "Switch the answerer, which answers your DMs when no agent is listening",
		ValidArgs: []string{"on", "off", "status"},
		Long: "With the answerer on, a top-level message you write in your DM with the bot, which no listening " +
			"agent takes, starts a fresh, read-only Claude Code run, and its answer is posted in the message's " +
			"thread. A reply in that thread is a follow-up. It is off until switched on here, and `off` stops a " +
			"run at once. Slack cannot switch it. See docs/threat-model.md, S6.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sw := ""
			if len(args) == 1 && args[0] != "status" {
				sw = args[0]
				if sw != "on" && sw != "off" {
					return fail(ExitUsage, "want on, off or status, not %q", sw)
				}
			}
			resp, err := ipc.Call(cmd.Context(), env.SocketPath, ipc.Request{Op: ipc.OpAnswerer, Switch: sw})
			if err != nil || !resp.OK {
				return callErr(err, resp)
			}
			_, _ = fmt.Fprintln(env.Stdout, answererLine(resp.Status.Answerer))
			return nil
		},
	}
}

// answererLine is the answerer's state on one line.
func answererLine(a ipc.AnswererStatus) string {
	if !a.On {
		return fmt.Sprintf("answerer: off; $%.2f of $%.2f spent today", a.Spent, a.Cap)
	}
	parts := []string{"answerer: on"}
	if a.Running {
		parts = append(parts, "answering")
	}
	if a.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", a.Queued))
	}
	return strings.Join(parts, ", ") + fmt.Sprintf("; $%.2f of $%.2f spent today", a.Spent, a.Cap)
}
