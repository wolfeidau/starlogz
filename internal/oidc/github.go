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
	redirectURI   string
	scope         string
	codeChallenge string
	clientState   string
	createdAt     time.Time
}

// pendingCode holds the identity, PKCE challenge, and GitHub App tokens waiting for the token exchange.
type pendingCode struct {
	sub                string // GitHub user ID as decimal string
	email              string
	scope              string
	codeChallenge      string
	redirectURI        string
	createdAt          time.Time
	accessToken        string
	refreshToken       string
	accessTokenExpiry  time.Time
	refreshTokenExpiry time.Time
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

// gitHubConnector abstracts the GitHub OAuth2 exchange so it can be replaced in tests.
type gitHubConnector interface {
	// AuthCodeURL returns the URL to redirect the user to for GitHub login.
	AuthCodeURL(state string) string
	// ExchangeCode exchanges an authorization code for a token and resolved identity.
	ExchangeCode(ctx context.Context, code string) (*oauth2.Token, *githubIdentity, error)
}

// oauthGitHubConnector is the production implementation backed by *oauth2.Config.
type oauthGitHubConnector struct {
	cfg *oauth2.Config
}

func newGitHubConnector(clientID, clientSecret, callbackURL string) *oauthGitHubConnector {
	return &oauthGitHubConnector{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       []string{"read:user", "user:email"},
			Endpoint:     githuboauth.Endpoint,
			RedirectURL:  callbackURL,
		},
	}
}

func (c *oauthGitHubConnector) AuthCodeURL(state string) string {
	return c.cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

func (c *oauthGitHubConnector) ExchangeCode(ctx context.Context, code string) (*oauth2.Token, *githubIdentity, error) {
	token, err := c.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("exchange code: %w", err)
	}
	identity, err := fetchGitHubIdentity(ctx, c.cfg.Client(ctx, token))
	if err != nil {
		return nil, nil, err
	}
	return token, identity, nil
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

// githubIdentity is the resolved identity returned by fetchGitHubIdentity.
type githubIdentity struct {
	ID    int64
	Email string
	Login string
}

// fetchGitHubIdentity returns the resolved GitHub identity for the authenticated user.
// It falls back to /user/emails when the profile email is not set (common for private accounts).
func fetchGitHubIdentity(ctx context.Context, client *http.Client) (*githubIdentity, error) {
	var user githubUser
	if err := githubGet(ctx, client, "https://api.github.com/user", &user); err != nil {
		return nil, fmt.Errorf("fetch GitHub user: %w", err)
	}

	if user.Email != "" {
		return &githubIdentity{ID: user.ID, Email: user.Email, Login: user.Login}, nil
	}

	var emails []githubEmail
	if err := githubGet(ctx, client, "https://api.github.com/user/emails", &emails); err != nil {
		return nil, fmt.Errorf("fetch GitHub emails: %w", err)
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return &githubIdentity{ID: user.ID, Email: e.Email, Login: user.Login}, nil
		}
	}

	return nil, fmt.Errorf("no verified primary email on GitHub account")
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

// extractRefreshExpiry reads the refresh_token_expires_in field from the GitHub App
// token response. Falls back to six months if the field is absent.
func extractRefreshExpiry(token *oauth2.Token) time.Time {
	if v := token.Extra("refresh_token_expires_in"); v != nil {
		if secs, ok := v.(float64); ok && secs > 0 {
			return time.Now().Add(time.Duration(secs) * time.Second)
		}
	}
	return time.Now().Add(6 * 30 * 24 * time.Hour)
}
