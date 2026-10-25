package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/wolfeidau/starlogz/internal/operations"
	"github.com/wolfeidau/starlogz/internal/server"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
	"github.com/wolfeidau/starlogz/internal/wideevent"
)

// ServerFlags is the server configuration shared by the http and lambda commands.
type ServerFlags struct {
	BaseServerURL                string        `help:"The base URL of this server." default:"http://localhost:8088" env:"SERVER_URL"`
	JWKPath                      string        `help:"Path to the JSON web key used to sign auth tokens." xor:"jwk"`
	JWKContent                   string        `help:"JSON content of the JSON web key used to sign auth tokens." env:"JWK_CONTENT" xor:"jwk"`
	GitHubClientID               string        `help:"GitHub OAuth2 application client ID." env:"GITHUB_CLIENT_ID" required:""`
	GitHubClientSecret           string        `help:"GitHub OAuth2 application client secret." env:"GITHUB_CLIENT_SECRET" required:""`
	CIMDEnabled                  bool          `help:"Enable OAuth Client ID Metadata Document support." env:"CIMD_ENABLED"`
	DatabaseURL                  string        `help:"PostgreSQL connection string." env:"DATABASE_URL" required:""`
	TokenEncryptionKey           string        `help:"Base64-encoded 32-byte key for encrypting stored GitHub tokens." env:"TOKEN_ENCRYPTION_KEY" required:""`
	TokenEncryptionKeyPrevious   string        `help:"Previous base64-encoded 32-byte token encryption key, accepted for decryption during key rotation." env:"TOKEN_ENCRYPTION_KEY_PREVIOUS"`
	RefreshTokenGracePeriod      time.Duration `help:"How long a rotated refresh token remains accepted for retry; use 0s to disable." default:"30s" env:"REFRESH_TOKEN_GRACE_PERIOD"`
	RetiredRefreshTokenRetention time.Duration `help:"How long hashed retired refresh tokens are retained for refresh diagnostics." default:"24h" env:"RETIRED_REFRESH_TOKEN_RETENTION"`
	UISessionIdleTTL             time.Duration `help:"How long an inactive web UI session remains valid." default:"168h" env:"UI_SESSION_IDLE_TTL"`
	UISessionTTL                 time.Duration `help:"Maximum lifetime of a web UI session." default:"720h" env:"UI_SESSION_TTL"`
	OperatorGitHubIDs            []int64       `help:"GitHub numeric IDs allowed to access service operations." env:"OPERATOR_GITHUB_IDS" sep:","`
	EventBusName                 string        `help:"EventBridge bus for privacy-safe wide events; empty disables publishing." env:"EVENT_BUS_NAME"`
	OperationsLogGroupName       string        `help:"CloudWatch log group queried for operator telemetry; empty disables telemetry." env:"OPERATIONS_LOG_GROUP_NAME"`
	Migrate                      bool          `help:"Run database migrations on start; when disabled, verify the schema is current instead." default:"true" negatable:"" env:"MIGRATE_ON_START"`
	Environment                  string        `help:"Deployment environment included in wide events." default:"local" env:"ENVIRONMENT"`
}

func (f *ServerFlags) newServer(ctx context.Context, globals *Globals, shutdownTimeout time.Duration) (*server.Server, func(), error) {
	jsonPrivateKey := []byte(f.JWKContent)
	switch {
	case f.JWKPath != "":
		var err error
		jsonPrivateKey, err = os.ReadFile(f.JWKPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read jwk file: %w", err)
		}
	case f.JWKContent == "":
		return nil, nil, fmt.Errorf("one of --jwk-path or JWK_CONTENT is required")
	}

	privkey, err := jwk.ParseKey(jsonPrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse jwk: %w", err)
	}

	encKey, err := decodeEncryptionKey("TOKEN_ENCRYPTION_KEY", f.TokenEncryptionKey)
	if err != nil {
		return nil, nil, err
	}
	var prevKey *[32]byte
	if f.TokenEncryptionKeyPrevious != "" {
		k, err := decodeEncryptionKey("TOKEN_ENCRYPTION_KEY_PREVIOUS", f.TokenEncryptionKeyPrevious)
		if err != nil {
			return nil, nil, err
		}
		prevKey = &k
	}

	st, err := postgres.New(ctx, f.DatabaseURL, store.NewEncryptor(encKey, prevKey))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	srv, err := f.buildServer(ctx, globals, st, privkey, shutdownTimeout)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return srv, st.Close, nil
}

func (f *ServerFlags) buildServer(ctx context.Context, globals *Globals, st *postgres.Store, privkey jwk.Key, shutdownTimeout time.Duration) (*server.Server, error) {
	start := time.Now()
	var err error
	if f.Migrate {
		err = st.Migrate(ctx, globals.Logger)
		if err != nil {
			return nil, fmt.Errorf("failed to migrate database: %w", err)
		}
	} else if err = st.CheckSchema(ctx, globals.Logger); err != nil {
		return nil, fmt.Errorf("failed to check database schema: %w", err)
	}
	schemaDuration := time.Since(start)

	var awsCfg aws.Config
	if f.EventBusName != "" || f.OperationsLogGroupName != "" {
		awsCfg, err = awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load AWS config: %w", err)
		}
	}

	var eventPublisher wideevent.EventPublisher = wideevent.NoopPublisher{}
	if f.EventBusName != "" {
		eventPublisher = wideevent.NewEventBridgePublisher(eventbridge.NewFromConfig(awsCfg), f.EventBusName)
	}
	var operationsTelemetry operations.Provider
	if f.OperationsLogGroupName != "" {
		operationsTelemetry = operations.NewCloudWatch(
			cloudwatchlogs.NewFromConfig(awsCfg), f.OperationsLogGroupName,
		)
	}
	serviceVersion := globals.Version
	if serviceVersion == "" {
		serviceVersion = "devel"
	}
	eventEmitter, err := wideevent.NewEmitter(eventPublisher, f.Environment, serviceVersion, globals.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to configure wide events: %w", err)
	}

	srv, err := server.New(server.Config{
		BaseURL:                      f.BaseServerURL,
		GitHubClientID:               f.GitHubClientID,
		GitHubClientSecret:           f.GitHubClientSecret,
		CIMDEnabled:                  f.CIMDEnabled,
		PrivKey:                      privkey,
		Logger:                       globals.Logger,
		Store:                        st,
		ShutdownTimeout:              shutdownTimeout,
		RefreshTokenGracePeriod:      &f.RefreshTokenGracePeriod,
		RetiredRefreshTokenRetention: &f.RetiredRefreshTokenRetention,
		UISessionIdleTTL:             f.UISessionIdleTTL,
		UISessionTTL:                 f.UISessionTTL,
		OperatorGitHubIDs:            f.OperatorGitHubIDs,
		OperationsTelemetry:          operationsTelemetry,
		SentryHandler:                globals.SentryHandler,
		Events:                       eventEmitter,
		Version:                      serviceVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create server: %w", err)
	}
	globals.Logger.InfoContext(ctx, "server initialized",
		slog.Int64("schema_ms", schemaDuration.Milliseconds()),
		slog.Int64("setup_ms", time.Since(start).Milliseconds()),
	)
	return srv, nil
}

func decodeEncryptionKey(name, value string) ([32]byte, error) {
	var key [32]byte
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 {
		return key, fmt.Errorf("%s must be a base64-encoded 32-byte value (use: openssl rand -base64 32)", name)
	}
	copy(key[:], b)
	return key, nil
}
