/*
Command hikyaku carries messages between coding agents and Slack.
*/
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ushineko/hikyaku/internal/cli"
)

func main() {
	env, err := cli.DefaultEnv()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "hikyaku:", err)
		os.Exit(cli.ExitError)
	}
	os.Exit(cli.Execute(context.Background(), env, os.Args[1:]))
}
