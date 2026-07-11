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

type githubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
	Bio       string `json:"bio"`
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
	// RefreshToken exchanges a GitHub refresh token for a new token pair and identity.
	RefreshToken(ctx context.Context, refreshToken string) (*oauth2.Token, *githubIdentity, error)
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
		return nil, nil, fmt.Errorf("fetch GitHub identity: %w", err)
	}
	return token, identity, nil
}

// RefreshToken posts to GitHub's token endpoint with grant_type=refresh_token.
// An empty AccessToken on the seed token forces the oauth2 library to call refresh.
func (c *oauthGitHubConnector) RefreshToken(ctx context.Context, refreshToken string) (*oauth2.Token, *githubIdentity, error) {
	src := c.cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	token, err := src.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("refresh GitHub token: %w", err)
	}
	identity, err := fetchGitHubIdentity(ctx, c.cfg.Client(ctx, token))
	if err != nil {
		return nil, nil, fmt.Errorf("fetch GitHub identity: %w", err)
	}
	return token, identity, nil
}

// githubIdentity is the resolved identity returned by fetchGitHubIdentity.
type githubIdentity struct {
	ID          int64
	Email       string
	Login       string
	DisplayName string
	AvatarURL   string
	ProfileURL  string
	Bio         string
}

// fetchGitHubIdentity returns the resolved GitHub identity for the authenticated user.
// It falls back to /user/emails when the profile email is not set (common for private accounts).
func fetchGitHubIdentity(ctx context.Context, client *http.Client) (*githubIdentity, error) {
	var user githubUser
	if err := githubGet(ctx, client, "https://api.github.com/user", &user); err != nil {
		return nil, fmt.Errorf("fetch GitHub user: %w", err)
	}

	identity := &githubIdentity{
		ID: user.ID, Email: user.Email, Login: user.Login, DisplayName: user.Name,
		AvatarURL: user.AvatarURL, ProfileURL: user.HTMLURL, Bio: user.Bio,
	}
	if user.Email != "" {
		return identity, nil
	}

	var emails []githubEmail
	if err := githubGet(ctx, client, "https://api.github.com/user/emails", &emails); err != nil {
		return nil, fmt.Errorf("fetch GitHub emails: %w", err)
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			identity.Email = e.Email
			return identity, nil
		}
	}

	return nil, fmt.Errorf("no verified primary email on GitHub account")
}

func githubGet(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
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
