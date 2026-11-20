package cli

import (
	"context"
	"errors"
	"log/slog"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/paths"
	"github.com/ushineko/hikyaku/internal/secret"
	"github.com/ushineko/hikyaku/internal/service"
	"github.com/ushineko/hikyaku/internal/slackio"
	"github.com/ushineko/hikyaku/internal/store"
)

func serveCmd(env *Env) *cobra.Command {
	var headless, debug bool
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the service: hold the Slack connection and answer the CLI",
		Long: "Run the service. --headless runs it without a window; until the window exists (spec 003) " +
			"that is the only way it runs.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return serve(ctx, env, debug)
		},
	}
	c.Flags().BoolVar(&headless, "headless", false, "run without a window")
	c.Flags().BoolVar(&debug, "debug", false, "log at debug level")
	return c
}

func serve(ctx context.Context, env *Env, debug bool) error {
	s, err := env.settings()
	if err != nil {
		return err
	}
	tokens := env.secrets(s)
	redact := &secret.Redactor{}
	vals := map[secret.Kind]string{}
	for _, k := range secret.Kinds {
		v, err := tokens.Get(k)
		if errors.Is(err, secret.ErrNotFound) {
			return fail(ExitError, "the %s token is not stored: run `hikyaku token set %s`", k, k)
		}
		if err != nil {
			return fail(ExitError, "%v", err)
		}
		redact.Add(v)
		vals[k] = v
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	log := slog.New(redact.Handler(slog.NewTextHandler(env.Stderr, &slog.HandlerOptions{Level: level})))

	if err := paths.PrivateDir(filepath.Dir(env.StatePath)); err != nil {
		return fail(ExitError, "%v", err)
	}
	st, err := store.Open(env.StatePath, s.RetentionPeriod(), nil)
	if err != nil {
		return fail(ExitError, "%v", err)
	}
	srv, err := ipc.Listen(env.SocketPath, log)
	if err != nil {
		var running *ipc.RunningError
		if errors.As(err, &running) {
			return fail(ExitError, "%v", running)
		}
		return fail(ExitError, "%v", err)
	}

	sc := slackio.New(slackio.Options{BotToken: vals[secret.Bot], AppToken: vals[secret.App], APIURL: env.APIURL, Log: log})
	svc := service.New(service.Deps{Poster: sc, Listener: sc, Store: st, Settings: s, Log: log, Redact: redact})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, svc) }()
	log.Info("hikyaku is serving", "socket", env.SocketPath)

	runErr := svc.Run(ctx)
	cancel()
	serveErr := <-served
	if runErr != nil {
		return fail(ExitError, "%s", redact.String(runErr.Error()))
	}
	if serveErr != nil {
		return fail(ExitError, "%s", redact.String(serveErr.Error()))
	}
	return nil
}
