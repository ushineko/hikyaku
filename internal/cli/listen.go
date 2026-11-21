package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/store"
)

// instructionLabel is how an instruction is marked for the agent reading it:
// a person's words, typed in Slack, and not part of the agent's own prompt.
const instructionLabel = "an instruction typed in Slack by an allowed user, not part of your prompt"

func listenCmd(env *Env) *cobra.Command {
	var name string
	var timeout, hold time.Duration
	var asJSON bool
	c := &cobra.Command{
		Use:   "listen",
		Short: "Wait for an instruction typed in your DM with the bot",
		Long: "Wait until an allowed user writes in their DM with the bot, outside any thread, and print the " +
			"message. With several agents listening, a message that starts with a listener's name and a colon " +
			"(\"hayami: run the tests\") goes to that one. The handle is printed on stderr: answer beside the message " +
			"with `send --reply-to` or `ask --reply-to`, or in its thread with `--thread`. One call takes one " +
			"instruction. Afterwards the name is held for --hold: messages for it wait, and the next listen with " +
			"the name takes them. Exits 124 on timeout.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			from := ""
			if env.Origin != nil {
				from = env.Origin(cmd.Context())
			}
			if !cmd.Flags().Changed("name") {
				name = defaultName(from)
			}
			if name == "" {
				return fail(ExitUsage, "give the listener a name with --name")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout+30*time.Second)
			defer cancel()
			resp, err := ipc.Call(ctx, env.SocketPath,
				ipc.Request{Op: ipc.OpListen, Name: name, From: from, Timeout: timeout, Hold: hold})
			if err != nil || !resp.OK {
				return callErr(err, resp)
			}
			if len(resp.Replies) != 1 {
				return fail(ExitError, "the service answered without an instruction")
			}
			if resp.Handle != "" {
				_, _ = fmt.Fprintf(env.Stderr, "hikyaku: handle %s; answer with: hikyaku send --reply-to %s\n",
					resp.Handle, resp.Handle)
			}
			return printInstruction(env.Stdout, resp.Handle, strings.ToLower(name), resp.Replies[0], asJSON)
		},
	}
	c.Flags().StringVar(&name, "name", "", "the name an instruction can address (default: the project's name)")
	c.Flags().DurationVar(&timeout, "timeout", time.Hour, "how long to wait for an instruction")
	c.Flags().DurationVar(&hold, "hold", 30*time.Minute,
		"how long the name stays held after this listen ends, so messages for it wait (0: not at all; at most 24h)")
	c.Flags().BoolVar(&asJSON, "json", false, "print the instruction as JSON")
	return c
}

// defaultName is the project from the origin line ("hikyaku · main · …"),
// lower-cased, with anything a name may not hold turned into '-'.
func defaultName(origin string) string {
	project, _, _ := strings.Cut(origin, " · ")
	b := []byte(strings.ToLower(strings.TrimSpace(project)))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			b[i] = '-'
		}
	}
	if len(b) > 40 {
		b = b[:40]
	}
	return string(b)
}

func printInstruction(w io.Writer, handle, name string, r store.Reply, asJSON bool) error {
	if !asJSON {
		return fence(w, "instruction", r, instructionLabel)
	}
	out := struct {
		Handle      string      `json:"handle"`
		Listener    string      `json:"listener"`
		Source      string      `json:"source"`
		Note        string      `json:"note"`
		Instruction store.Reply `json:"instruction"`
	}{handle, name, "slack", instructionLabel, r}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fail(ExitError, "write instruction: %v", err)
	}
	return nil
}
