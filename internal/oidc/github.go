package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	githuboauth "golang.org/x/oauth2/github"
)

// pendingAuth holds the client's PKCE and redirect params while the user is at GitHub.
type pendingAuth struct {
	clientID      string
	redirectURI   string
	scope         string
	codeChallenge string
	clientState   string
	createdAt     time.Time
}

// pendingCode holds the identity and PKCE challenge waiting for the token exchange.
type pendingCode struct {
	sub           string // GitHub user ID as decimal string
	email         string
	scope         string
	codeChallenge string
	redirectURI   string
	clientID      string
	createdAt     time.Time
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Email string `json:"email"`
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func newGitHubOAuthConfig(clientID, clientSecret, callbackURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     githuboauth.Endpoint,
		RedirectURL:  callbackURL,
	}
}

func (s *Server) storePending(githubState string, p *pendingAuth) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pending[githubState] = p
	now := time.Now()
	for k, v := range s.pending {
		if now.Sub(v.createdAt) > 10*time.Minute {
			delete(s.pending, k)
		}
	}
}

func (s *Server) consumePending(githubState string) (*pendingAuth, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p, ok := s.pending[githubState]
	if !ok {
		return nil, false
	}
	delete(s.pending, githubState)
	if time.Since(p.createdAt) > 10*time.Minute {
		return nil, false
	}
	return p, true
}

func (s *Server) storeCode(code string, pc *pendingCode) {
	s.codesMu.Lock()
	defer s.codesMu.Unlock()
	s.codes[code] = pc
	now := time.Now()
	for k, v := range s.codes {
		if now.Sub(v.createdAt) > 5*time.Minute {
			delete(s.codes, k)
		}
	}
}

func (s *Server) consumeCode(code string) (*pendingCode, bool) {
	s.codesMu.Lock()
	defer s.codesMu.Unlock()
	pc, ok := s.codes[code]
	if !ok {
		return nil, false
	}
	delete(s.codes, code)
	if time.Since(pc.createdAt) > 5*time.Minute {
		return nil, false
	}
	return pc, true
}

// fetchGitHubIdentity returns the GitHub user's numeric ID, primary verified email, and login.
// It falls back to /user/emails when the profile email is not set (common for private accounts).
func fetchGitHubIdentity(ctx context.Context, client *http.Client) (int64, string, string, error) {
	var user githubUser
	if err := githubGet(ctx, client, "https://api.github.com/user", &user); err != nil {
		return 0, "", "", fmt.Errorf("fetch GitHub user: %w", err)
	}

	if user.Email != "" {
		return user.ID, user.Email, user.Login, nil
	}

	var emails []githubEmail
	if err := githubGet(ctx, client, "https://api.github.com/user/emails", &emails); err != nil {
		return 0, "", "", fmt.Errorf("fetch GitHub emails: %w", err)
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return user.ID, e.Email, user.Login, nil
		}
	}

	return 0, "", "", fmt.Errorf("no verified primary email on GitHub account")
}

func githubGet(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned %d for %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
