package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wolfeidau/starlogz/internal/middleware"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type HTTPCmd struct {
	ListenAddr    string `help:"The address to listen on." default:"localhost:8088" env:"HTTP_LISTEN_ADDR"`
	BaseServerURL string `help:"The url this base server URL." default:"http://localhost:8088" env:"SERVER_URL"`
	JWKPath       string `help:"The path of the JSON web key used to sign auth tokens."`
}

func (c *HTTPCmd) Run(ctx context.Context, globals *Globals) error {
	mcpServer := NewMCPServer(globals.Logger)

	jsonPrivateKey, err := os.ReadFile(c.JWKPath)
	if err != nil {
		return fmt.Errorf("failed to read jwk file: %w", err)
	}

	privkey, err := jwk.ParseKey(jsonPrivateKey)
	if err != nil {
		return fmt.Errorf("failed to parse jwk: %w", err)
	}

	oidcServer, err := oidc.NewServer(c.BaseServerURL, privkey)
	if err != nil {
		return fmt.Errorf("failed to create oidc server: %w", err)
	}

	jwtAuth := auth.RequireBearerToken(oidcServer.VerifyJWT, &auth.RequireBearerTokenOptions{
		Scopes:              []string{"facts:read"},
		ResourceMetadataURL: oidcServer.ResourceMetadataURL(),
	})

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpServer.server
	}, nil)

	authenticatedHandler := jwtAuth(handler)
	metadata := oidcServer.ProtectedResourceMeta()

	listener, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", c.ListenAddr, err)
	}

	mux := http.NewServeMux()

	mux.Handle("/.well-known/oauth-authorization-server", oidcServer.DiscoveryHandler())
	mux.Handle("/.well-known/openid-configuration", oidcServer.DiscoveryHandler())
	mux.Handle("/.well-known/jwks", oidcServer.JWKSHandler())
	mux.Handle("/.well-known/oauth-protected-resource", auth.ProtectedResourceMetadataHandler(metadata))
	mux.Handle("/oauth2/register", oidcServer.DCRHandler())
	mux.Handle("/oauth2/authorize", oidcServer.AuthorizeHandler())
	mux.Handle("/oauth2/token", oidcServer.TokenHandler())
	mux.Handle("/auth/github/callback", oidcServer.GitHubCallbackHandler())
	mux.Handle("/auth/logout", oidcServer.LogoutHandler())
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/mcp", authenticatedHandler)

	srv := newServerWithTimeouts(
		otelhttp.NewHandler(middleware.AccessLog(globals.Logger)(mux), ServerName),
		30*time.Second,
	)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

type MCPServer struct {
	logger *slog.Logger
	server *mcp.Server
}

func NewMCPServer(logger *slog.Logger) *MCPServer {
	ms := &MCPServer{
		logger: logger,
		server: mcp.NewServer(&mcp.Implementation{Name: ServerName}, nil),
	}

	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "whoami",
		Description: "Returns identity, org memberships, and token scopes. Agents should call this first to verify they have the right access before writing.",
	}, ms.Whoami)

	return ms
}

func (ms *MCPServer) Whoami(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
	userInfo := req.Extra.TokenInfo

	logger := ms.logger.With(
		slog.String("user_id", userInfo.UserID),
	)

	type whoamiresp struct {
		UserID string   `json:"user_id"`
		Scopes []string `json:"scopes"`
	}

	jsonData, err := json.Marshal(&whoamiresp{
		UserID: userInfo.UserID,
		Scopes: userInfo.Scopes,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal user data: %w", err)
	}

	logger.Info("whoami call")

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(jsonData),
			},
		},
	}, nil, nil
}

func newServerWithTimeouts(handler http.Handler, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	}); err != nil {
		slog.Default().Error("failed to write health response", slog.Any("error", err))
	}
}
