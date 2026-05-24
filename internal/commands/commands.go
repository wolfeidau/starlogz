package commands

import (
	"log/slog"
	"net/http"
)

type Globals struct {
	Logger        *slog.Logger
	SentryHandler func(http.Handler) http.Handler
}
