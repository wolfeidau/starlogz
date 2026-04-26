package middleware

import "net/http"

// CORS adds permissive cross-origin headers required for browser-based OAuth2 clients
// (e.g. the MCP Inspector). Handles OPTIONS preflight requests with a 204 short-circuit
// so they never reach authentication or business logic.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
