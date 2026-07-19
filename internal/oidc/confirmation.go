package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/wideevent"
	"golang.org/x/net/idna"
)

const (
	confirmationPath            = "/oauth2/authorize/confirm"
	confirmationFormContentType = "application/x-www-form-urlencoded"
	confirmationDecisionApprove = "approve"
	confirmationDecisionDeny    = "deny"
)

// FirstPartyDashboardClientID is the sole client allowed to bypass authorization confirmation.
const FirstPartyDashboardClientID = "starlogz-ui"

var authorizationConfirmationBypassClientIDs = map[string]struct{}{
	FirstPartyDashboardClientID: {},
}

var authorizationConfirmationTemplate = template.Must(template.New("authorization-confirmation").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Authorize access · Starlogz</title>
  <style nonce="{{.Nonce}}">
    :root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, sans-serif; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #111827; color: #e5e7eb; }
    main { width: min(38rem, calc(100% - 2rem)); box-sizing: border-box; padding: 2rem; border: 1px solid #374151; border-radius: .75rem; background: #1f2937; }
    h1 { margin-top: 0; font-size: 1.5rem; } h2 { margin: 1.5rem 0 .5rem; font-size: 1rem; }
    code { overflow-wrap: anywhere; } ul { padding-left: 1.25rem; } li + li { margin-top: .75rem; }
    .muted { color: #9ca3af; } .actions { display: flex; gap: .75rem; margin-top: 2rem; }
    button { border: 0; border-radius: .4rem; padding: .7rem 1rem; font: inherit; cursor: pointer; }
    .approve { background: #2563eb; color: white; } .deny { background: #4b5563; color: white; }
  </style>
  <script nonce="{{.Nonce}}">
    history.replaceState(null, document.title, "` + confirmationPath + `");
    document.addEventListener("DOMContentLoaded", function () {
      const form = document.getElementById("authorization-confirmation");
      let submitted = false;
      form.addEventListener("submit", function (event) {
        if (submitted) {
          event.preventDefault();
          return;
        }
        const submitter = event.submitter;
        if (!submitter) return;
        submitted = true;
        const decision = document.createElement("input");
        decision.type = "hidden";
        decision.name = "decision";
        decision.value = submitter.value;
        form.appendChild(decision);
        submitter.removeAttribute("name");
        form.querySelectorAll("button[type=submit]").forEach(function (button) { button.disabled = true; });
      });
    });
  </script>
</head>
<body>
<main>
  <h1>Authorize {{.DisplayName}}</h1>
  {{if .Unnamed}}<p class="muted">Client ID: <code>{{.ClientID}}</code></p>{{end}}
  <p>This client is requesting access to Starlogz.</p>
  <h2>Redirect destination</h2>
  <code>{{.RedirectURI}}</code>
  <h2>Requested permissions</h2>
  <ul>{{range .Scopes}}<li><code>{{.Name}}</code><br><span class="muted">{{.Description}}</span></li>{{end}}</ul>
  <form id="authorization-confirmation" method="post" action="` + confirmationPath + `">
    <input type="hidden" name="token" value="{{.Token}}">
    <div class="actions">
      <button class="approve" type="submit" name="decision" value="` + confirmationDecisionApprove + `">Continue</button>
      <button class="deny" type="submit" name="decision" value="` + confirmationDecisionDeny + `">Cancel</button>
    </div>
  </form>
</main>
</body>
</html>`))

type confirmationScope struct {
	Name        string
	Description string
}

type confirmationPageData struct {
	Nonce       string
	Token       string
	DisplayName string
	Unnamed     bool
	ClientID    string
	RedirectURI string
	Scopes      []confirmationScope
}

var scopeDescriptions = map[string]string{
	scopeInsightsRead:  "Read projects and insights.",
	scopeInsightsWrite: "Create, update, restore, and delete insights.",
	scopeOrgAdmin:      "Administer organizations when organization tools become available.",
}

func newConfirmationToken() (string, []byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256(raw[:])
	return token, hash[:], nil
}

func authorizationConfirmationRequired(clientID string) bool {
	_, bypass := authorizationConfirmationBypassClientIDs[clientID]
	return !bypass
}

func renderAuthorizationConfirmation(w http.ResponseWriter, pending *store.PendingAuth, token string) error {
	var nonceRaw [18]byte
	if _, err := rand.Read(nonceRaw[:]); err != nil {
		return fmt.Errorf("generate CSP nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw[:])
	formActionSource, err := authorizationConfirmationFormActionSource(pending.RedirectURI)
	if err != nil {
		return fmt.Errorf("build confirmation form-action source: %w", err)
	}
	displayName := pending.ClientName
	unnamed := displayName == ""
	if unnamed {
		displayName = "Unnamed client"
	}
	scopes := make([]confirmationScope, 0, len(strings.Fields(pending.Scope)))
	for _, name := range strings.Fields(pending.Scope) {
		scopes = append(scopes, confirmationScope{Name: name, Description: scopeDescriptions[name]})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self' "+formActionSource+"; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'")
	return authorizationConfirmationTemplate.Execute(w, confirmationPageData{
		Nonce: nonce, Token: token, DisplayName: displayName, Unnamed: unnamed,
		ClientID: pending.ClientID, RedirectURI: pending.RedirectURI, Scopes: scopes,
	})
}

func authorizationConfirmationFormActionSource(redirectURI string) (string, error) {
	redirectTo, err := url.Parse(redirectURI)
	if err != nil || !redirectTo.IsAbs() || redirectTo.User != nil {
		return "", fmt.Errorf("invalid redirect URI")
	}
	scheme := strings.ToLower(redirectTo.Scheme)
	if scheme != redirectSchemeHTTP && scheme != redirectSchemeHTTPS {
		return scheme + ":", nil
	}
	host := redirectTo.Hostname()
	if host == "" {
		return "", fmt.Errorf("redirect URI has no host")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return "", fmt.Errorf("normalize redirect URI host: %w", err)
		}
	}
	if port := redirectTo.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

// AuthorizationConfirmationHandler completes the post-GitHub client confirmation.
func (s *Server) AuthorizationConfirmationHandler() http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawQuery != "" {
			writeConfirmationError(w, http.StatusBadRequest)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != confirmationFormContentType {
			writeConfirmationError(w, http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := r.ParseForm(); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeConfirmationError(w, http.StatusRequestEntityTooLarge)
				return
			}
			writeConfirmationError(w, http.StatusBadRequest)
			return
		}
		if len(r.PostForm) != 2 || len(r.PostForm["token"]) != 1 || len(r.PostForm["decision"]) != 1 {
			writeConfirmationError(w, http.StatusBadRequest)
			return
		}
		rawToken, err := base64.RawURLEncoding.DecodeString(r.PostForm["token"][0])
		if err != nil || len(rawToken) != 32 {
			writeConfirmationError(w, http.StatusBadRequest)
			return
		}
		decision := r.PostForm["decision"][0]
		if decision != confirmationDecisionApprove && decision != confirmationDecisionDeny {
			writeConfirmationError(w, http.StatusBadRequest)
			return
		}
		tokenHash := sha256.Sum256(rawToken)
		approve := decision == confirmationDecisionApprove
		code := ""
		if approve {
			code = uuid.New().String()
		}
		result, err := s.authState.CompleteAuthorizationConfirmation(r.Context(), tokenHash[:], approve, code)
		if errors.Is(err, store.ErrNotFound) {
			writeConfirmationError(w, http.StatusBadRequest)
			return
		}
		if err != nil {
			ctxlog.LoggerFrom(r.Context()).ErrorContext(r.Context(), "complete authorization confirmation failed")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		redirectTo, err := authorizationResultRedirect(result, approve, code)
		if err != nil {
			ctxlog.LoggerFrom(r.Context()).ErrorContext(r.Context(), "invalid stored authorization redirect")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, redirectTo, http.StatusSeeOther) //nolint:gosec // URI was validated before persistence.
	})

	crossOrigin := http.NewCrossOriginProtection()
	crossOrigin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeConfirmationError(w, http.StatusForbidden)
	}))
	protected := crossOrigin.Handler(handler)
	noStore := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		protected.ServeHTTP(w, r)
	})
	return s.events.HTTPHandler(wideevent.OAuthAuthorizationConfirmationCompleted, noStore)
}

func authorizationResultRedirect(result *store.AuthorizationConfirmationResult, approve bool, code string) (string, error) {
	redirectTo, err := url.Parse(result.RedirectURI)
	if err != nil {
		return "", err
	}
	preserved, err := preservedAuthorizationRedirectQuery(redirectTo.RawQuery)
	if err != nil {
		return "", err
	}
	response := url.Values{}
	if approve {
		response.Set(oauthCode, code)
	} else {
		response.Set("error", "access_denied")
	}
	if result.ClientState != "" {
		response.Set("state", result.ClientState)
	}
	preserved = append(preserved, response.Encode())
	redirectTo.RawQuery = strings.Join(preserved, "&")
	return redirectTo.String(), nil
}

func preservedAuthorizationRedirectQuery(rawQuery string) ([]string, error) {
	if rawQuery == "" {
		return nil, nil
	}
	parts := strings.Split(rawQuery, "&")
	preserved := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		rawName, _, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			return nil, fmt.Errorf("decode redirect query parameter name: %w", err)
		}
		if isReservedAuthorizationResponseParameter(name) {
			continue
		}
		preserved = append(preserved, part)
	}
	return preserved, nil
}

func isReservedAuthorizationResponseParameter(name string) bool {
	switch name {
	case oauthCode, "error", "error_description", "state":
		return true
	default:
		return false
	}
}

func writeConfirmationError(w http.ResponseWriter, status int) {
	http.Error(w, "invalid authorization confirmation", status)
}
