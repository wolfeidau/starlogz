package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/wolfeidau/starlogz/internal/server"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
	"github.com/wolfeidau/starlogz/internal/wideevent"
)

type HTTPCmd struct {
	ListenAddr                   string        `help:"The address to listen on." default:"localhost:8088" env:"HTTP_LISTEN_ADDR"`
	BaseServerURL                string        `help:"The base URL of this server." default:"http://localhost:8088" env:"SERVER_URL"`
	JWKPath                      string        `help:"Path to the JSON web key used to sign auth tokens." required:""`
	GitHubClientID               string        `help:"GitHub OAuth2 application client ID." env:"GITHUB_CLIENT_ID" required:""`
	GitHubClientSecret           string        `help:"GitHub OAuth2 application client secret." env:"GITHUB_CLIENT_SECRET" required:""`
	DatabaseURL                  string        `help:"PostgreSQL connection string." env:"DATABASE_URL" required:""`
	TokenEncryptionKey           string        `help:"Base64-encoded 32-byte key for encrypting stored GitHub tokens." env:"TOKEN_ENCRYPTION_KEY" required:""`
	ShutdownTimeout              time.Duration `help:"Maximum time to wait for in-flight requests before exiting." default:"30s" env:"SHUTDOWN_TIMEOUT"`
	RefreshTokenGracePeriod      time.Duration `help:"How long a rotated refresh token remains accepted for retry; use 0s to disable." default:"30s" env:"REFRESH_TOKEN_GRACE_PERIOD"`
	RetiredRefreshTokenRetention time.Duration `help:"How long hashed retired refresh tokens are retained for refresh diagnostics." default:"24h" env:"RETIRED_REFRESH_TOKEN_RETENTION"`
	UISessionIdleTTL             time.Duration `help:"How long an inactive web UI session remains valid." default:"168h" env:"UI_SESSION_IDLE_TTL"`
	UISessionTTL                 time.Duration `help:"Maximum lifetime of a web UI session." default:"720h" env:"UI_SESSION_TTL"`
	EventBusName                 string        `help:"EventBridge bus for privacy-safe wide events; empty disables publishing." env:"EVENT_BUS_NAME"`
	Environment                  string        `help:"Deployment environment included in wide events." default:"local" env:"ENVIRONMENT"`
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

	keyBytes, err := base64.StdEncoding.DecodeString(c.TokenEncryptionKey)
	if err != nil || len(keyBytes) != 32 {
		return fmt.Errorf("TOKEN_ENCRYPTION_KEY must be a base64-encoded 32-byte value (use: openssl rand -base64 32)")
	}
	var encKey [32]byte
	copy(encKey[:], keyBytes)

	st, err := postgres.New(ctx, c.DatabaseURL, store.NewEncryptor(encKey))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer st.Close()

	err = st.Migrate(ctx, globals.Logger)
	if err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	var eventPublisher wideevent.EventPublisher = wideevent.NoopPublisher{}
	if c.EventBusName != "" {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return fmt.Errorf("failed to load AWS config for EventBridge: %w", err)
		}
		eventPublisher = wideevent.NewEventBridgePublisher(eventbridge.NewFromConfig(awsCfg), c.EventBusName)
	}
	serviceVersion := globals.Version
	if serviceVersion == "" {
		serviceVersion = "devel"
	}
	eventEmitter, err := wideevent.NewEmitter(eventPublisher, c.Environment, serviceVersion, globals.Logger)
	if err != nil {
		return fmt.Errorf("failed to configure wide events: %w", err)
	}

	srv, err := server.New(server.Config{
		BaseURL:                      c.BaseServerURL,
		GitHubClientID:               c.GitHubClientID,
		GitHubClientSecret:           c.GitHubClientSecret,
		PrivKey:                      privkey,
		Logger:                       globals.Logger,
		Store:                        st,
		ShutdownTimeout:              c.ShutdownTimeout,
		RefreshTokenGracePeriod:      &c.RefreshTokenGracePeriod,
		RetiredRefreshTokenRetention: &c.RetiredRefreshTokenRetention,
		UISessionIdleTTL:             c.UISessionIdleTTL,
		UISessionTTL:                 c.UISessionTTL,
		SentryHandler:                globals.SentryHandler,
		Events:                       eventEmitter,
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
