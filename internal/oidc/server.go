package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"github.com/wolfeidau/starlogz/internal/store"
)

// UserUpserter persists user identity on successful GitHub login.
// Implemented by store.Store via *postgres.Store.
type UserUpserter interface {
	UpsertUser(ctx context.Context, githubID int64, email, login string) (*store.User, error)
}

// GrantStore persists authorization grants with associated GitHub App tokens.
// RotateGrant atomically swaps the grant row and records the old jti as revoked,
// so a failure of either step leaves both tokens valid (current state) rather
// than the new token live and the old one un-revoked.
type GrantStore interface {
	UpsertGrant(ctx context.Context, g store.Grant) error
	GetGrant(ctx context.Context, jti string) (*store.Grant, error)
	GetGrantByRefreshToken(ctx context.Context, token string) (*store.Grant, error)
	GetRetiredRefreshToken(ctx context.Context, tokenHash []byte) (*store.RetiredRefreshToken, error)
	RotateGrant(ctx context.Context, oldToken, oldJTI string, oldJWTExpiry time.Time, g store.Grant, retired *store.RetiredRefreshToken) (*store.Grant, error)
	DeleteGrant(ctx context.Context, jti string, retired *store.RetiredRefreshToken) error
}

// AuthStateStore persists transient OAuth2 authorization state.
type AuthStateStore interface {
	StorePendingAuth(ctx context.Context, state string, p store.PendingAuth) error
	ConsumePendingAuth(ctx context.Context, state string) (*store.PendingAuth, error)
	StoreAuthCode(ctx context.Context, code string, c store.AuthCode) error
	ConsumeAuthCode(ctx context.Context, code string) (*store.AuthCode, error)
}

// RevocationStore persists revoked JWT IDs.
type RevocationStore interface {
	RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error
	IsTokenRevoked(ctx context.Context, jti string) (bool, error)
}

// Config holds construction parameters for Server.
type Config struct {
	BaseURL                      string
	GitHubClientID               string
	GitHubClientSecret           string
	Users                        UserUpserter    // optional; nil skips user persistence
	Clients                      ClientStore     // optional; nil skips DCR persistence
	Grants                       GrantStore      // optional; nil skips grant persistence
	AuthState                    AuthStateStore  // required
	Revocation                   RevocationStore // required
	RefreshTokenGracePeriod      *time.Duration  // optional; nil defaults to 30s, 0 disables grace retry
	RetiredRefreshTokenRetention *time.Duration  // optional; nil defaults to 24h
}

// Server is the OAuth2/OIDC authorization server for the MCP endpoint.
type Server struct {
	baseURL                      *url.URL
	privkey                      jwk.Key
	pubkey                       jwk.Key
	jwksJSON                     []byte
	authMeta                     *oauthex.AuthServerMeta
	resMeta                      *oauthex.ProtectedResourceMetadata
	github                       gitHubConnector
	users                        UserUpserter
	clients                      ClientStore
	grants                       GrantStore
	authState                    AuthStateStore
	revocation                   RevocationStore
	refreshTokenGracePeriod      time.Duration
	retiredRefreshTokenRetention time.Duration
	logger                       *slog.Logger
}

// NewServer constructs an OIDC Server from config and a loaded private key.
func NewServer(cfg Config, privkey jwk.Key) (*Server, error) {
	if cfg.AuthState == nil {
		return nil, fmt.Errorf("Config.AuthState is required")
	}
	if cfg.Revocation == nil {
		return nil, fmt.Errorf("Config.Revocation is required")
	}
	refreshTokenGracePeriod, retiredRefreshTokenRetention, err := resolveRefreshTokenDurations(cfg)
	if err != nil {
		return nil, err
	}

	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base URL: %w", err)
	}

	pubkey, err := jwk.PublicKeyOf(privkey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive public key: %w", err)
	}

	if err := jwk.AssignKeyID(pubkey); err != nil {
		return nil, fmt.Errorf("failed to assign key ID: %w", err)
	}

	if err := pubkey.Set(jwk.KeyUsageKey, "sig"); err != nil {
		return nil, fmt.Errorf("failed to set key usage: %w", err)
	}
	if err := pubkey.Set(jwk.AlgorithmKey, jwa.ES384()); err != nil {
		return nil, fmt.Errorf("failed to set key algorithm: %w", err)
	}

	kid, ok := pubkey.KeyID()
	if !ok {
		return nil, fmt.Errorf("key ID not set after assignment")
	}

	if err = privkey.Set(jwk.KeyIDKey, kid); err != nil {
		return nil, fmt.Errorf("failed to propagate key ID to private key: %w", err)
	}

	set := jwk.NewSet()
	if err = set.AddKey(pubkey); err != nil {
		return nil, fmt.Errorf("failed to build JWKS: %w", err)
	}

	jwksJSON, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JWKS: %w", err)
	}

	return &Server{
		baseURL:                      base,
		privkey:                      privkey,
		pubkey:                       pubkey,
		jwksJSON:                     jwksJSON,
		authMeta:                     buildAuthServerMeta(base),
		resMeta:                      buildProtectedResourceMeta(base),
		github:                       newGitHubConnector(cfg.GitHubClientID, cfg.GitHubClientSecret, base.JoinPath("/auth/github/callback").String()),
		users:                        cfg.Users,
		clients:                      cfg.Clients,
		grants:                       cfg.Grants,
		authState:                    cfg.AuthState,
		revocation:                   cfg.Revocation,
		refreshTokenGracePeriod:      refreshTokenGracePeriod,
		retiredRefreshTokenRetention: retiredRefreshTokenRetention,
		logger:                       slog.Default().With(slog.String("component", "oidc")),
	}, nil
}

func resolveRefreshTokenDurations(cfg Config) (time.Duration, time.Duration, error) {
	gracePeriod := DefaultRefreshTokenGracePeriod
	if cfg.RefreshTokenGracePeriod != nil {
		gracePeriod = *cfg.RefreshTokenGracePeriod
	}
	if gracePeriod < 0 {
		return 0, 0, fmt.Errorf("refresh token grace period must be >= 0")
	}
	if gracePeriod > maxRefreshTokenGracePeriod {
		return 0, 0, fmt.Errorf("refresh token grace period must be <= %s", maxRefreshTokenGracePeriod)
	}

	retention := DefaultRetiredRefreshTokenRetention
	if cfg.RetiredRefreshTokenRetention != nil {
		retention = *cfg.RetiredRefreshTokenRetention
	}
	if retention <= 0 {
		return 0, 0, fmt.Errorf("retired refresh token retention must be > 0")
	}
	if retention < gracePeriod {
		return 0, 0, fmt.Errorf("retired refresh token retention must be >= refresh token grace period")
	}
	return gracePeriod, retention, nil
}

// AuthServerMeta returns the pre-built OAuth2 authorization server metadata.
func (s *Server) AuthServerMeta() *oauthex.AuthServerMeta {
	return s.authMeta
}

// ProtectedResourceMeta returns the pre-built OAuth2 protected resource metadata.
func (s *Server) ProtectedResourceMeta() *oauthex.ProtectedResourceMetadata {
	return s.resMeta
}

// ResourceMetadataURL returns the URL of the protected resource metadata endpoint.
func (s *Server) ResourceMetadataURL() string {
	return s.baseURL.JoinPath("/.well-known/oauth-protected-resource").String()
}

// VerifyJWT validates a bearer token and returns auth info.
func (s *Server) VerifyJWT(ctx context.Context, tokenString string, _ *http.Request) (*auth.TokenInfo, error) {
	verifiedToken, err := jwt.ParseString(tokenString, jwt.WithKey(jwa.ES384(), s.pubkey))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	iss, ok := verifiedToken.Issuer()
	if !ok || iss != s.baseURL.String() {
		return nil, fmt.Errorf("%w: invalid issuer", auth.ErrInvalidToken)
	}

	aud, ok := verifiedToken.Audience()
	if !ok {
		return nil, fmt.Errorf("%w: missing aud claim", auth.ErrInvalidToken)
	}
	resourceURL := s.baseURL.JoinPath("/mcp").String()
	audValid := false
	for _, a := range aud {
		if a == resourceURL {
			audValid = true
			break
		}
	}
	if !audValid {
		return nil, fmt.Errorf("%w: token audience does not include resource", auth.ErrInvalidToken)
	}

	var jti string
	if err := verifiedToken.Get("jti", &jti); err != nil || jti == "" {
		return nil, fmt.Errorf("%w: missing jti claim", auth.ErrInvalidToken)
	}

	ctx = ctxlog.WithLogger(ctx, ctxlog.LoggerFrom(ctx).With(slog.String("jti", jti)))

	revoked, err := s.revocation.IsTokenRevoked(ctx, jti)
	if err != nil {
		return nil, fmt.Errorf("%w: revocation check failed: %v", auth.ErrInvalidToken, err)
	}
	if revoked {
		return nil, fmt.Errorf("%w: token has been revoked", auth.ErrInvalidToken)
	}

	var scope string
	if err := verifiedToken.Get("scope", &scope); err != nil {
		return nil, fmt.Errorf("%w: invalid token claims", auth.ErrInvalidToken)
	}

	if scope == "" {
		return nil, fmt.Errorf("%w: missing scope claim", auth.ErrInvalidToken)
	}

	expiresAt, ok := verifiedToken.Expiration()
	if !ok {
		return nil, fmt.Errorf("%w: missing expiration claim", auth.ErrInvalidToken)
	}

	sub, ok := verifiedToken.Subject()
	if !ok || sub == "" {
		return nil, fmt.Errorf("%w: missing sub claim", auth.ErrInvalidToken)
	}

	return &auth.TokenInfo{
		UserID:     sub,
		Scopes:     strings.Fields(scope),
		Expiration: expiresAt,
	}, nil
}

// IssueJWT signs and returns a new ES384 JWT for the given subject, email, scope,
// JWT ID and expiration. The caller owns expiration so the value matches what's
// recorded in the grant row and the revoked_tokens entry.
func (s *Server) IssueJWT(sub, email, scope, jti string, exp time.Time) (string, error) {
	tok, err := jwt.NewBuilder().
		Issuer(s.baseURL.String()).
		Subject(sub).
		IssuedAt(time.Now()).
		Expiration(exp).
		Audience([]string{s.baseURL.JoinPath("/mcp").String()}).
		Claim("email", email).
		Claim("scope", scope).
		Claim("jti", jti).
		Build()
	if err != nil {
		return "", fmt.Errorf("build token: %w", err)
	}

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES384(), s.privkey))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	return string(signed), nil
}
