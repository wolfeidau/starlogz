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
	"github.com/wolfeidau/starlogz/internal/store"
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
	Store              store.Store // nil is allowed; fact tools will return an error
}

// Server is the configured HTTP server ready to serve requests.
type Server struct {
	handler http.Handler
	logger  *slog.Logger
	store   store.Store
}

// New builds the mux, wires all handlers, and returns a Server.
func New(cfg Config) (*Server, error) {
	oidcServer, err := oidc.NewServer(oidc.Config{
		BaseURL:            cfg.BaseURL,
		GitHubClientID:     cfg.GitHubClientID,
		GitHubClientSecret: cfg.GitHubClientSecret,
		Users:              cfg.Store,
		Clients:            cfg.Store,
		Grants:             cfg.Store,
	}, cfg.PrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create oidc server: %w", err)
	}

	srv := &Server{logger: cfg.Logger, store: cfg.Store}

	mcpSrv := newMCPServer(cfg.Logger, cfg.Store)

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
	mux.HandleFunc("/health", srv.healthHandler)
	// DELETE intercept: go-sdk returns 204 which browser Fetch API rejects in Service Workers.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		authenticatedHandler.ServeHTTP(w, r)
	})

	srv.handler = otelhttp.NewHandler(
		middleware.CORS(middleware.AccessLog(cfg.Logger)(mux)),
		name,
	)

	return srv, nil
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

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	health := map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	}
	if s.store != nil {
		if err := s.store.Ping(r.Context()); err != nil {
			health["status"] = "degraded"
			health["db"] = "error"
		} else {
			health["db"] = "ok"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(health); err != nil {
		slog.Default().Error("failed to write health response", slog.Any("error", err))
	}
}
