package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecurityHeaders(t *testing.T) {
	tests := []struct {
		name     string
		https    bool
		wantHSTS string
	}{
		{name: "http", https: false},
		{name: "https", https: true, wantHSTS: "max-age=31536000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := SecurityHeaders(test.https)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/missing", nil))

			require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
			require.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
			require.Equal(t, "0", w.Header().Get("X-XSS-Protection"))
			require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
			require.Equal(t, "camera=(), geolocation=(), microphone=(), payment=(), usb=()", w.Header().Get("Permissions-Policy"))
			require.Equal(t, contentSecurityPolicy, w.Header().Get("Content-Security-Policy"))
			require.Equal(t, test.wantHSTS, w.Header().Get("Strict-Transport-Security"))
		})
	}
}
