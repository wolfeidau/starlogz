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
	_ "time/tzdata" // zone lookup on Lambda without system zoneinfo

	"github.com/alecthomas/kong"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/lmittmann/tint"
	"github.com/wolfeidau/starlogz/internal/commands"
	"github.com/wolfeidau/starlogz/internal/logattr"
	"github.com/wolfeidau/starlogz/internal/secrets"
	"github.com/wolfeidau/starlogz/internal/telemetry"
)

var (
	version = "devel"
	cli     struct {
		HTTP        commands.HTTPCmd    `cmd:"" help:"http mcp server using streamable HTTP transport."`
		Lambda      commands.LambdaCmd  `cmd:"" help:"serve API Gateway HTTP API events from the Lambda runtime."`
		Migrate     commands.MigrateCmd `cmd:"" help:"run database migrations and exit."`
		Keygen      commands.KeyGenCmd  `cmd:"" help:"generate json web key to sign auth tokens."`
		Development bool
		Version     kong.VersionFlag
	}
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ssmStart := time.Now()
	if err := loadSSMSecrets(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "starlogz-server: %v\n", err)
		stop()
		os.Exit(1) //nolint:gocritic // stop() is called explicitly above
	}
	ssmDuration := time.Since(ssmStart)

	cmd := kong.Parse(&cli,
		kong.Name("starlogz-server"),
		kong.Description("A server that gives agents memory."),
		kong.UsageOnError(),
		kong.Vars{
			"version": version,
		},
		kong.BindTo(ctx, (*context.Context)(nil)),
	)

	cmd.FatalIfErrorf(run(ctx, cmd, cli.Development, ssmDuration))
}

// Must run before kong.Parse because kong env tags and Sentry init read the environment.
func loadSSMSecrets(ctx context.Context) error {
	path := os.Getenv("SSM_SECRETS_PATH")
	if path == "" {
		return nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}
	return secrets.LoadEnv(ctx, ssm.NewFromConfig(cfg), path)
}

func newLogger(development, sentryEnabled bool) *slog.Logger {
	level := slog.LevelInfo
	if development {
		level = slog.LevelDebug
	}
	if configured := os.Getenv("LOG_LEVEL"); configured != "" {
		if err := level.UnmarshalText([]byte(configured)); err != nil {
			fmt.Fprintf(os.Stderr, "invalid LOG_LEVEL %q; using %s\n", configured, level)
		}
	}

	var handler slog.Handler
	if development {
		handler = tint.NewTextHandler(os.Stderr, &tint.Options{
			Level:      level,
			TimeFormat: time.Kitchen,
		})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	}

	handler = telemetry.NewOTelHandler(handler)
	if sentryEnabled {
		handler = slog.NewMultiHandler(handler, telemetry.NewSentrySlogHandler())
	}
	handler = logattr.NewPrivacyHandler(handler)
	return slog.New(handler)
}

func run(ctx context.Context, cmd *kong.Context, development bool, ssmDuration time.Duration) error {
	buildInfo, _ := debug.ReadBuildInfo()

	sentryStart := time.Now()
	sentryShutdown, sentryEnabled, err := telemetry.InitSentry(ctx, "starlogz-server", version)
	if err != nil {
		return err
	}
	sentryDuration := time.Since(sentryStart)

	logger := newLogger(development, sentryEnabled)
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

	telemetryStart := time.Now()
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
	child.Info("startup timings",
		slog.Int64("ssm_ms", ssmDuration.Milliseconds()),
		slog.Int64("sentry_ms", sentryDuration.Milliseconds()),
		slog.Int64("telemetry_ms", time.Since(telemetryStart).Milliseconds()),
	)

	var sentryHandler func(http.Handler) http.Handler
	if sentryEnabled {
		sentryHandler = telemetry.NewSentryHTTPHandler()
	}

	return cmd.Run(&commands.Globals{Logger: child, SentryHandler: sentryHandler, Version: version})
}
