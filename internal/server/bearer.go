package server

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// The SDK derives the challenge scope from the enforced scopes, so a read-only
// minimum would steer clients to request read-only tokens.
const mcpChallengeScope = "insights:read insights:write"

// mcpBearerAuth requires insights:read but challenges for both scopes.
func mcpBearerAuth(verifier auth.TokenVerifier, resourceMetadataURL string) func(http.Handler) http.Handler {
	challenge := fmt.Sprintf("Bearer resource_metadata=%q, scope=%q", resourceMetadataURL, mcpChallengeScope)
	verify := auth.RequireBearerToken(verifier, nil)

	return func(next http.Handler) http.Handler {
		scoped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !slices.Contains(auth.TokenInfoFromContext(r.Context()).Scopes, "insights:read") {
				http.Error(w, "insufficient scope", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
		inner := verify(scoped)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.ServeHTTP(&challengeWriter{ResponseWriter: w, challenge: challenge}, r)
		})
	}
}

type challengeWriter struct {
	http.ResponseWriter
	challenge string
}

func (w *challengeWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		w.Header().Set("WWW-Authenticate", w.challenge)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *challengeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
