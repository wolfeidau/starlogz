package commands

import "log/slog"

const ServerName = "starlogz-mcp-server"

type Globals struct {
	Logger *slog.Logger
}
