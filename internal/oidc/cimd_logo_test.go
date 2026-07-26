package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseEligibleCIMDLogoURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "https", rawURL: "https://assets.example.com/logo.png"},
		{name: "query", rawURL: "https://cdn.example.com/logo.png?version=2"},
		{name: "relative", rawURL: "/logo.png", wantErr: true},
		{name: "http", rawURL: "http://assets.example.com/logo.png", wantErr: true},
		{name: "userinfo", rawURL: "https://user@assets.example.com/logo.png", wantErr: true},
		{name: "fragment", rawURL: "https://assets.example.com/logo.png#fragment", wantErr: true},
		{name: "port", rawURL: "https://assets.example.com:8443/logo.png", wantErr: true},
		{name: "IP literal", rawURL: "https://203.0.113.1/logo.png", wantErr: true},
		{name: "missing path", rawURL: "https://assets.example.com", wantErr: true},
		{name: "oversized", rawURL: "https://assets.example.com/" + strings.Repeat("a", maxCIMDLogoURILen), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseEligibleCIMDLogoURL(test.rawURL)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestResolveCIMDLogoSanitizesPNGAndJPEG(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "PNG", contentType: "image/png", body: encodeTestPNG(t, 256, 128)},
		{name: "JPEG", contentType: "image/jpeg", body: encodeTestJPEG(t, 256, 128)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := testLogoResolver(func(*http.Request) (*http.Response, error) {
				return logoResponse(http.StatusOK, test.contentType, test.body), nil
			})
			got, reason := resolver.resolveLogo(t.Context(), "https://assets.example.com/logo")
			require.Empty(t, reason)
			require.NotEmpty(t, got)
			require.LessOrEqual(t, len(got), maxCIMDLogoOutputSize)
			config, format, err := image.DecodeConfig(bytes.NewReader(got))
			require.NoError(t, err)
			require.Equal(t, "png", format)
			require.Equal(t, 128, config.Width)
			require.Equal(t, 64, config.Height)
		})
	}
}

func TestResolveCIMDLogoSoftFailures(t *testing.T) {
	validPNG := encodeTestPNG(t, 32, 32)
	tooWidePNG := encodeTestPNG(t, maxCIMDLogoDimension+1, 1)
	tests := []struct {
		name        string
		status      int
		contentType string
		body        []byte
		wantReason  string
	}{
		{name: "status", status: http.StatusNotFound, contentType: "image/png", body: validPNG, wantReason: cimdLogoOmittedStatus},
		{name: "redirect", status: http.StatusFound, contentType: "image/png", body: validPNG, wantReason: cimdLogoOmittedStatus},
		{name: "SVG", status: http.StatusOK, contentType: "image/svg+xml", body: []byte("<svg/>"), wantReason: cimdLogoOmittedContentType},
		{name: "GIF", status: http.StatusOK, contentType: "image/gif", body: []byte("GIF89a"), wantReason: cimdLogoOmittedContentType},
		{name: "WebP", status: http.StatusOK, contentType: "image/webp", body: []byte("RIFF"), wantReason: cimdLogoOmittedContentType},
		{name: "MIME mismatch", status: http.StatusOK, contentType: "image/jpeg", body: validPNG, wantReason: cimdLogoOmittedContentType},
		{name: "oversized response", status: http.StatusOK, contentType: "image/png", body: make([]byte, maxCIMDLogoResponseSize+1), wantReason: cimdLogoOmittedSize},
		{name: "truncated image", status: http.StatusOK, contentType: "image/png", body: validPNG[:len(validPNG)/2], wantReason: cimdLogoOmittedDecode},
		{name: "dimensions", status: http.StatusOK, contentType: "image/png", body: tooWidePNG, wantReason: cimdLogoOmittedDimensions},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := testLogoResolver(func(*http.Request) (*http.Response, error) {
				return logoResponse(test.status, test.contentType, test.body), nil
			})
			got, reason := resolver.resolveLogo(t.Context(), "https://assets.example.com/logo.png")
			require.Empty(t, got)
			require.Equal(t, test.wantReason, reason)
		})
	}
}

func TestResolveCIMDLogoAbsentInvalidAndNetworkFailure(t *testing.T) {
	requests := 0
	resolver := testLogoResolver(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unavailable")
	})

	got, reason := resolver.resolveLogo(t.Context(), "")
	require.Empty(t, got)
	require.Empty(t, reason)
	require.Zero(t, requests)

	got, reason = resolver.resolveLogo(t.Context(), "javascript:alert(1)")
	require.Empty(t, got)
	require.Equal(t, cimdLogoOmittedInvalidURL, reason)
	require.Zero(t, requests)

	got, reason = resolver.resolveLogo(t.Context(), "https://assets.example.com/logo.png")
	require.Empty(t, got)
	require.Equal(t, cimdLogoOmittedNetwork, reason)
	require.Equal(t, 1, requests)
}

func TestResolveCIMDLogoRejectsPrivateAndReboundAddresses(t *testing.T) {
	t.Run("private target", func(t *testing.T) {
		resolver := &httpClientIDMetadataResolver{
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("request must not be sent")
				return nil, nil
			})},
			lookupIP: func(context.Context, string, string) ([]net.IP, error) {
				return []net.IP{net.IPv4(127, 0, 0, 1).To4()}, nil
			},
		}
		got, reason := resolver.resolveLogo(t.Context(), "https://assets.example.com/logo.png")
		require.Empty(t, got)
		require.Equal(t, cimdLogoOmittedNetwork, reason)
	})

	t.Run("DNS rebinding", func(t *testing.T) {
		lookups := 0
		lookupIP := func(context.Context, string, string) ([]net.IP, error) {
			lookups++
			if lookups == 1 {
				return []net.IP{net.IPv4(93, 184, 216, 34).To4()}, nil
			}
			return []net.IP{net.IPv4(127, 0, 0, 1).To4()}, nil
		}
		resolver := &httpClientIDMetadataResolver{
			client:   newCIMDHTTPClient(lookupIP),
			lookupIP: lookupIP,
		}
		got, reason := resolver.resolveLogo(t.Context(), "https://assets.example.com/logo.png")
		require.Empty(t, got)
		require.Equal(t, cimdLogoOmittedNetwork, reason)
		require.GreaterOrEqual(t, lookups, 2)
	})
}

func TestResolveCIMDLogoTimeoutIncludesPreflightDNS(t *testing.T) {
	resolver := &httpClientIDMetadataResolver{
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("request must not be sent")
			return nil, nil
		})},
		lookupIP: func(ctx context.Context, _, _ string) ([]net.IP, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.WithinDuration(t, time.Now().Add(cimdLogoTimeout), deadline, 100*time.Millisecond)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	started := time.Now()
	got, reason := resolver.resolveLogo(t.Context(), "https://assets.example.com/logo.png")
	require.Empty(t, got)
	require.Equal(t, cimdLogoOmittedNetwork, reason)
	require.GreaterOrEqual(t, time.Since(started), cimdLogoTimeout-100*time.Millisecond)
}

func TestResolveCIMDDocumentContinuesWhenLogoFails(t *testing.T) {
	clientID := "https://client.example.com/oauth/client-metadata.json"
	metadata, err := json.Marshal(map[string]any{
		"client_id":     clientID,
		"client_name":   "Example Client",
		"redirect_uris": []string{"https://client.example.com/callback"},
		"logo_uri":      "https://assets.example.com/logo.png",
	})
	require.NoError(t, err)

	resolver := testLogoResolver(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == clientID {
			return logoResponse(http.StatusOK, "application/json", metadata), nil
		}
		return logoResponse(http.StatusBadGateway, "image/png", nil), nil
	})
	client, err := resolver.Resolve(t.Context(), clientID)
	require.NoError(t, err)
	require.Empty(t, client.ClientLogoPNG)
	require.Equal(t, cimdLogoOmittedStatus, client.ClientLogoOmittedReason)
}

func testLogoResolver(roundTrip roundTripFunc) *httpClientIDMetadataResolver {
	return &httpClientIDMetadataResolver{
		client: &http.Client{Transport: roundTrip},
		lookupIP: func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.IPv4(93, 184, 216, 34).To4()}, nil
		},
	}
}

func logoResponse(status int, contentType string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func encodeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := testImage(width, height)
	var body bytes.Buffer
	require.NoError(t, png.Encode(&body, img))
	return body.Bytes()
}

func encodeTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := testImage(width, height)
	var body bytes.Buffer
	require.NoError(t, jpeg.Encode(&body, img, &jpeg.Options{Quality: 90}))
	return body.Bytes()
}

func testImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	return img
}
