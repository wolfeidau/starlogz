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
	starlogzv1connect "github.com/wolfeidau/starlogz/api/gen/proto/go/starlogz/v1/starlogzv1connect"
	"github.com/wolfeidau/starlogz/internal/middleware"
	"github.com/wolfeidau/starlogz/internal/oidc"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/wideevent"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const name = "starlogz-mcp-server"

// Config holds all parameters needed to construct the server.
type Config struct {
	BaseURL                      string
	GitHubClientID               string
	GitHubClientSecret           string
	PrivKey                      jwk.Key
	Logger                       *slog.Logger
	Store                        store.Store          // nil is allowed; fact tools will return an error
	AuthState                    oidc.AuthStateStore  // if nil, Store is used
	Revocation                   oidc.RevocationStore // if nil, Store is used
	ShutdownTimeout              time.Duration
	RefreshTokenGracePeriod      *time.Duration
	RetiredRefreshTokenRetention *time.Duration
	UISessionIdleTTL             time.Duration
	UISessionTTL                 time.Duration
	SentryHandler                func(http.Handler) http.Handler
	Events                       *wideevent.Emitter
}

// Server is the configured HTTP server ready to serve requests.
type Server struct {
	handler          http.Handler
	logger           *slog.Logger
	store            store.Store
	shutdownTimeout  time.Duration
	uiSessionIdleTTL time.Duration
	uiSessionTTL     time.Duration
	events           *wideevent.Emitter
}

// New builds the mux, wires all handlers, and returns a Server.
func New(cfg Config) (*Server, error) {
	shutdownTimeout := cfg.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 30 * time.Second
	}
	uiSessionIdleTTL := cfg.UISessionIdleTTL
	if uiSessionIdleTTL <= 0 {
		uiSessionIdleTTL = defaultUISessionIdleTTL
	}
	uiSessionTTL := cfg.UISessionTTL
	if uiSessionTTL <= 0 {
		uiSessionTTL = defaultUISessionTTL
	}
	if uiSessionIdleTTL > uiSessionTTL {
		return nil, fmt.Errorf("UI session idle TTL must not exceed absolute TTL")
	}

	authState := cfg.AuthState
	if authState == nil {
		authState = cfg.Store
	}
	revocation := cfg.Revocation
	if revocation == nil {
		revocation = cfg.Store
	}
	eventEmitter := cfg.Events
	if eventEmitter == nil {
		eventEmitter = wideevent.NewNoopEmitter()
	}
	oidcServer, err := oidc.NewServer(oidc.Config{
		BaseURL:                      cfg.BaseURL,
		GitHubClientID:               cfg.GitHubClientID,
		GitHubClientSecret:           cfg.GitHubClientSecret,
		Users:                        cfg.Store,
		Clients:                      cfg.Store,
		Grants:                       cfg.Store,
		AuthState:                    authState,
		Revocation:                   revocation,
		RefreshTokenGracePeriod:      cfg.RefreshTokenGracePeriod,
		RetiredRefreshTokenRetention: cfg.RetiredRefreshTokenRetention,
		Events:                       eventEmitter,
	}, cfg.PrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create oidc server: %w", err)
	}

	srv := &Server{
		logger: cfg.Logger, store: cfg.Store, shutdownTimeout: shutdownTimeout,
		uiSessionIdleTTL: uiSessionIdleTTL, uiSessionTTL: uiSessionTTL,
		events: eventEmitter,
	}

	mcpSrv := newMCPServer(cfg.Store, eventEmitter)

	jwtAuth := auth.RequireBearerToken(oidcServer.VerifyJWT, &auth.RequireBearerTokenOptions{
		Scopes:              []string{"insights:read"},
		ResourceMetadataURL: oidcServer.ResourceMetadataURL(),
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpSrv.server
	}, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		DisableLocalhostProtection: true,
	})

	authenticatedHandler := jwtAuth(mcpHandler)
	metadata := oidcServer.ProtectedResourceMeta()

	mux := http.NewServeMux()
	uiPath, uiHandler := starlogzv1connect.NewUIServiceHandler(newUIService(cfg.Store))
	mux.Handle(uiPath, srv.uiAuthMiddleware(uiHandler))
	mux.Handle("/public/", publicHandler())
	mux.HandleFunc("/", srv.redirectIfSession(pageHandler("starlogz")).ServeHTTP)
	mux.HandleFunc("/dashboard", pageHandler("starlogz dashboard"))
	mux.HandleFunc("/login", srv.loginHandler(cfg.BaseURL))
	mux.HandleFunc("/logout", srv.uiLogoutHandler())
	mux.HandleFunc("/ui/auth/callback", srv.uiCallbackHandler(oidcServer, cfg.BaseURL))
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

	handler := middleware.CORS(middleware.AccessLog(cfg.Logger)(mux))
	if cfg.SentryHandler != nil {
		handler = cfg.SentryHandler(handler)
	}
	srv.handler = otelhttp.NewHandler(handler, name)

	return srv, nil
}

// Handler returns the root HTTP handler. Use this with httptest.NewServer in tests.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Run serves on l until ctx is cancelled, then shuts down gracefully.
// Sequence: stop accepting → drain requests (shutdownTimeout) → close DB pool.
func (s *Server) Run(ctx context.Context, l net.Listener) error {
	httpSrv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	if err := httpSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
