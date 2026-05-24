package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInitSentry_NoDSN(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")

	shutdown, enabled, err := InitSentry(context.Background(), "starlogz-server", "test")
	require.NoError(t, err)
	require.False(t, enabled)
	require.NoError(t, shutdown(context.Background()))
}

func TestInitSentry_WithDSN(t *testing.T) {
	t.Setenv("SENTRY_DSN", "https://public@example.com/1")
	t.Setenv("SENTRY_ENVIRONMENT", "test")

	shutdown, enabled, err := InitSentry(context.Background(), "starlogz-server", "test")
	require.NoError(t, err)
	require.True(t, enabled)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
}

func TestInitSentry_InvalidDSN(t *testing.T) {
	t.Setenv("SENTRY_DSN", "://not-a-dsn")

	shutdown, enabled, err := InitSentry(context.Background(), "starlogz-server", "test")
	require.Error(t, err)
	require.False(t, enabled)
	require.Nil(t, shutdown)
}
