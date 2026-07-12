package main

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLoggerLevel(t *testing.T) {
	tests := []struct {
		name        string
		development bool
		configured  string
		debug       bool
		info        bool
	}{
		{name: "production defaults to info", info: true},
		{name: "development defaults to debug", development: true, debug: true, info: true},
		{name: "configured level overrides default", development: true, configured: "WARN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tt.configured)
			logger := newLogger(t.Context(), tt.development, false)
			require.Equal(t, tt.debug, logger.Enabled(t.Context(), slog.LevelDebug))
			require.Equal(t, tt.info, logger.Enabled(t.Context(), slog.LevelInfo))
			require.True(t, logger.Enabled(t.Context(), slog.LevelWarn))
		})
	}
}
