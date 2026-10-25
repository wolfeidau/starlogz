package commands

import (
	"context"
	"fmt"
	"net"
	"time"
)

type HTTPCmd struct {
	ServerFlags `embed:""`

	ListenAddr      string        `help:"The address to listen on." default:"localhost:8088" env:"HTTP_LISTEN_ADDR"`
	ShutdownTimeout time.Duration `help:"Maximum time to wait for in-flight requests before exiting." default:"30s" env:"SHUTDOWN_TIMEOUT"`
}

func (c *HTTPCmd) Run(ctx context.Context, globals *Globals) error {
	srv, cleanup, err := c.newServer(ctx, globals, c.ShutdownTimeout)
	if err != nil {
		return err
	}
	defer cleanup()

	l, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", c.ListenAddr, err)
	}

	return srv.Run(ctx, l)
}
