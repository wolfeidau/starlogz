package main

import (
	"context"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/alecthomas/kong"
	"github.com/wolfeidau/starlogz/internal/commands"
)

var (
	version = "devel"
	cli     struct {
		HTTP    commands.HTTPCmd `cmd:"" help:"http mcp server using streamable HTTP transport."`
		Version kong.VersionFlag
	}
)

func main() {
	ctx := context.Background()
	cmd := kong.Parse(&cli,
		kong.Name("starlogz-server"),
		kong.Description("A server that gives agents memory."),
		kong.UsageOnError(),
		kong.Vars{
			"version": version,
		},
		kong.BindTo(ctx, (*context.Context)(nil)),
	)

	cmd.FatalIfErrorf(run(ctx, cmd))
}

func run(_ context.Context, cmd *kong.Context) error {
	handler := slog.NewJSONHandler(os.Stdout, nil)
	buildInfo, _ := debug.ReadBuildInfo()

	logger := slog.New(handler)

	child := logger.With(
		slog.Group("program_info",
			slog.Int("pid", os.Getpid()),
			slog.String("go_version", buildInfo.GoVersion),
			slog.String("version", version),
		),
	)
	child.Info("starlogz server started")

	return cmd.Run(&commands.Globals{Logger: child})
}
