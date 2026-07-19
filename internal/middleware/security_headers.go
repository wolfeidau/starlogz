package middleware

import "net/http"

const contentSecurityPolicy = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' https://avatars.githubusercontent.com; connect-src 'self'"

// SecurityHeaders applies browser security policy to every HTTP response.
func SecurityHeaders(https bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("X-XSS-Protection", "0")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
			h.Set("Content-Security-Policy", contentSecurityPolicy)
			if https {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
