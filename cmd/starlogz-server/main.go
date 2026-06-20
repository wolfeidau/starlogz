package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/lmittmann/tint"
	"github.com/wolfeidau/starlogz/internal/commands"
	"github.com/wolfeidau/starlogz/internal/telemetry"
)

var (
	version = "devel"
	cli     struct {
		HTTP        commands.HTTPCmd    `cmd:"" help:"http mcp server using streamable HTTP transport."`
		Migrate     commands.MigrateCmd `cmd:"" help:"run database migrations and exit."`
		Keygen      commands.KeyGenCmd  `cmd:"" help:"generate json web key to sign auth tokens."`
		Development bool
		Version     kong.VersionFlag
	}
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd := kong.Parse(&cli,
		kong.Name("starlogz-server"),
		kong.Description("A server that gives agents memory."),
		kong.UsageOnError(),
		kong.Vars{
			"version": version,
		},
		kong.BindTo(ctx, (*context.Context)(nil)),
	)

	cmd.FatalIfErrorf(run(ctx, cmd, cli.Development))
}

func newLogger(ctx context.Context, development, sentryEnabled bool) *slog.Logger {
	var handler slog.Handler
	if development {
		handler = tint.NewHandler(os.Stderr, &tint.Options{
			Level:      slog.LevelDebug,
			TimeFormat: time.Kitchen,
		})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	}

	handler = telemetry.NewOTelHandler(handler)
	if sentryEnabled {
		handler = slog.NewMultiHandler(handler, telemetry.NewSentrySlogHandler(ctx))
	}
	return slog.New(handler)
}

func run(ctx context.Context, cmd *kong.Context, development bool) error {
	buildInfo, _ := debug.ReadBuildInfo()

	sentryShutdown, sentryEnabled, err := telemetry.InitSentry(ctx, "starlogz-server", version)
	if err != nil {
		return err
	}

	logger := newLogger(ctx, development, sentryEnabled)
	slog.SetDefault(logger)

	child := logger.With(
		slog.Group("program_info",
			slog.Int("pid", os.Getpid()),
			slog.String("go_version", buildInfo.GoVersion),
			slog.String("version", version),
		),
	)
	child.Info("starlogz server started")
	if sentryEnabled {
		child.Info("sentry enabled")
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := sentryShutdown(shutdownCtx); err != nil {
			child.Error("sentry shutdown error", slog.Any("error", err))
		}
	}()

	telShutdown, err := telemetry.InitTelemetry(ctx, "starlogz-server", version)
	if err != nil {
		return fmt.Errorf("failed to initialize telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := telShutdown(shutdownCtx); err != nil {
			child.Error("telemetry shutdown error", slog.Any("error", err))
		}
	}()

	var sentryHandler func(http.Handler) http.Handler
	if sentryEnabled {
		sentryHandler = telemetry.NewSentryHTTPHandler()
	}

	return cmd.Run(&commands.Globals{Logger: child, SentryHandler: sentryHandler})
}
