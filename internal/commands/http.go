package commands

import "context"

type HTTPCmd struct {
	ListenAddr string `help:"The address to listen on." default:"localhost:3000" env:"HTTP_LISTEN_ADDR"`
}

func (c *HTTPCmd) Run(ctx context.Context, globals *Globals) error {
	return nil
}
