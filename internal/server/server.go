package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wolfeidau/starlogz/internal/middleware"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const name = "starlogz-mcp-server"

// Config holds all parameters needed to construct the server.
// Accepts a parsed jwk.Key so callers (tests or CLI) can supply keys however they like.
type Config struct {
	BaseURL            string
	GitHubClientID     string
	GitHubClientSecret string
	PrivKey            jwk.Key
	Logger             *slog.Logger
}

// Server is the configured HTTP server ready to serve requests.
type Server struct {
	handler http.Handler
	logger  *slog.Logger
}

// New builds the mux, wires all handlers, and returns a Server.
func New(cfg Config) (*Server, error) {
	oidcServer, err := oidc.NewServer(oidc.Config{
		BaseURL:            cfg.BaseURL,
		GitHubClientID:     cfg.GitHubClientID,
		GitHubClientSecret: cfg.GitHubClientSecret,
	}, cfg.PrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create oidc server: %w", err)
	}

	mcpSrv := newMCPServer(cfg.Logger)

	jwtAuth := auth.RequireBearerToken(oidcServer.VerifyJWT, &auth.RequireBearerTokenOptions{
		Scopes:              []string{"facts:read"},
		ResourceMetadataURL: oidcServer.ResourceMetadataURL(),
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpSrv.server
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	authenticatedHandler := jwtAuth(mcpHandler)
	metadata := oidcServer.ProtectedResourceMeta()

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
	// DELETE intercept: go-sdk returns 204 which browser Fetch API rejects in Service Workers.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		authenticatedHandler.ServeHTTP(w, r)
	})

	handler := otelhttp.NewHandler(
		middleware.CORS(middleware.AccessLog(cfg.Logger)(mux)),
		name,
	)

	return &Server{handler: handler, logger: cfg.Logger}, nil
}

// Handler returns the root HTTP handler. Use this with httptest.NewServer in tests.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Run serves on l until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context, l net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

type mcpServer struct {
	logger *slog.Logger
	server *mcp.Server
}

func newMCPServer(logger *slog.Logger) *mcpServer {
	ms := &mcpServer{
		logger: logger,
		server: mcp.NewServer(&mcp.Implementation{Name: name}, nil),
	}
	mcp.AddTool(ms.server, &mcp.Tool{
		Name:        "whoami",
		Description: "Returns identity, org memberships, and token scopes. Agents should call this first to verify they have the right access before writing.",
	}, ms.whoami)
	return ms
}

func (ms *mcpServer) whoami(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	userInfo := req.Extra.TokenInfo

	ms.logger.InfoContext(ctx, "whoami call", slog.String("user_id", userInfo.UserID))

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

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonData)}},
	}, nil, nil
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
