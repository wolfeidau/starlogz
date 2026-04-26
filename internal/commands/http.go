package commands

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/wolfeidau/starlogz/internal/server"
	"github.com/wolfeidau/starlogz/internal/store"
)

type HTTPCmd struct {
	ListenAddr         string `help:"The address to listen on." default:"localhost:8088" env:"HTTP_LISTEN_ADDR"`
	BaseServerURL      string `help:"The base URL of this server." default:"http://localhost:8088" env:"SERVER_URL"`
	JWKPath            string `help:"Path to the JSON web key used to sign auth tokens." required:""`
	GitHubClientID     string `help:"GitHub OAuth2 application client ID." env:"GITHUB_CLIENT_ID" required:""`
	GitHubClientSecret string `help:"GitHub OAuth2 application client secret." env:"GITHUB_CLIENT_SECRET" required:""`
	DatabaseURL        string `help:"PostgreSQL connection string." env:"DATABASE_URL" required:""`
}

func (c *HTTPCmd) Run(ctx context.Context, globals *Globals) error {
	jsonPrivateKey, err := os.ReadFile(c.JWKPath)
	if err != nil {
		return fmt.Errorf("failed to read jwk file: %w", err)
	}

	privkey, err := jwk.ParseKey(jsonPrivateKey)
	if err != nil {
		return fmt.Errorf("failed to parse jwk: %w", err)
	}

	st, err := store.New(ctx, c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx, globals.Logger); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	srv, err := server.New(server.Config{
		BaseURL:            c.BaseServerURL,
		GitHubClientID:     c.GitHubClientID,
		GitHubClientSecret: c.GitHubClientSecret,
		PrivKey:            privkey,
		Logger:             globals.Logger,
		Store:              st,
	})
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	l, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", c.ListenAddr, err)
	}

	return srv.Run(ctx, l)
}
