package oidc

import (
	"time"

	"golang.org/x/oauth2"
)

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
