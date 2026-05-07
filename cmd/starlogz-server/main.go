package main

import (
	"context"
	"fmt"
	"log/slog"
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
		HTTP        commands.HTTPCmd   `cmd:"" help:"http mcp server using streamable HTTP transport."`
		Keygen      commands.KeyGenCmd `cmd:"" help:"generate json web key to sign auth tokens."`
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

func newLogger(development bool) *slog.Logger {
	if development {
		return slog.New(telemetry.NewOTelHandler(tint.NewHandler(os.Stderr, &tint.Options{
			Level:      slog.LevelDebug,
			TimeFormat: time.Kitchen,
		})))
	}
	return slog.New(telemetry.NewOTelHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
}

func run(ctx context.Context, cmd *kong.Context, development bool) error {
	buildInfo, _ := debug.ReadBuildInfo()

	logger := newLogger(development)
	slog.SetDefault(logger)

	child := logger.With(
		slog.Group("program_info",
			slog.Int("pid", os.Getpid()),
			slog.String("go_version", buildInfo.GoVersion),
			slog.String("version", version),
		),
	)
	child.Info("starlogz server started")

	telShutdown, err := telemetry.InitTelemetry(ctx, "starlogz-server", version)
	if err != nil {
		return fmt.Errorf("failed to initialize telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telShutdown(shutdownCtx); err != nil {
			child.Error("telemetry shutdown error", slog.Any("error", err))
		}
	}()

	return cmd.Run(&commands.Globals{Logger: child})
}
