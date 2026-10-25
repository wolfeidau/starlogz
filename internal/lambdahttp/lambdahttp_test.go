package lambdahttp_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/lambdahttp"
)

func event(method, path string) events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{
		RawPath: path,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			DomainName: "api.example.com",
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method:   method,
				SourceIP: "203.0.113.7",
				Protocol: "HTTP/1.1",
			},
		},
	}
}

func invoke(t *testing.T, h http.HandlerFunc, e events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	t.Helper()
	resp, err := lambdahttp.Handler(h)(t.Context(), e)
	require.NoError(t, err)
	return resp
}

func TestRequestMapping(t *testing.T) {
	e := event(http.MethodPost, "/a%2Fb/c")
	e.RawQueryString = "x=1&y=a%20b"
	e.Headers = map[string]string{
		"host":              "svc.example.com",
		"x-forwarded-proto": "http",
		"x-custom-header":   "v",
	}

	var got *http.Request
	invoke(t, func(_ http.ResponseWriter, r *http.Request) { got = r }, e)

	require.Equal(t, http.MethodPost, got.Method)
	require.Equal(t, "/a%2Fb/c", got.URL.EscapedPath())
	require.Equal(t, "/a/b/c", got.URL.Path)
	require.Equal(t, "x=1&y=a%20b", got.URL.RawQuery)
	require.Equal(t, "a b", got.URL.Query().Get("y"))
	require.Equal(t, "v", got.Header.Get("X-Custom-Header"))
	require.Equal(t, "svc.example.com", got.Host)
	require.Equal(t, "http", got.Header.Get("X-Forwarded-Proto"))
	require.Equal(t, "/a%2Fb/c?x=1&y=a%20b", got.RequestURI)
	require.Empty(t, got.URL.Host)
	require.Empty(t, got.URL.Scheme)
	require.Equal(t, "203.0.113.7:0", got.RemoteAddr)
}

func TestRequestDefaults(t *testing.T) {
	var got *http.Request
	invoke(t, func(_ http.ResponseWriter, r *http.Request) { got = r }, event(http.MethodGet, "/x"))

	require.Equal(t, "api.example.com", got.Host)
	require.Empty(t, got.URL.RawQuery)
}

func TestRequestDoubleSlashPathIsNotAuthority(t *testing.T) {
	var got *http.Request
	invoke(t, func(_ http.ResponseWriter, r *http.Request) { got = r }, event(http.MethodGet, "//evil.example/x"))

	require.Equal(t, "api.example.com", got.Host)
	require.Equal(t, "//evil.example/x", got.URL.Path)
}

func TestRequestCookies(t *testing.T) {
	e := event(http.MethodGet, "/")
	e.Cookies = []string{"a=1", "b=2"}

	var a, b *http.Cookie
	invoke(t, func(_ http.ResponseWriter, r *http.Request) {
		var err error
		a, err = r.Cookie("a")
		require.NoError(t, err)
		b, err = r.Cookie("b")
		require.NoError(t, err)
	}, e)

	require.Equal(t, "1", a.Value)
	require.Equal(t, "2", b.Value)
}

func TestRequestBody(t *testing.T) {
	tests := map[string]struct {
		body    string
		encoded bool
	}{
		"plain":  {body: "hello", encoded: false},
		"base64": {body: base64.StdEncoding.EncodeToString([]byte("hello")), encoded: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			e := event(http.MethodPost, "/")
			e.Body = tt.body
			e.IsBase64Encoded = tt.encoded

			var body string
			var length int64
			invoke(t, func(_ http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				body, length = string(b), r.ContentLength
			}, e)

			require.Equal(t, "hello", body)
			require.Equal(t, int64(5), length)
		})
	}
}

func TestRequestMalformedBase64(t *testing.T) {
	e := event(http.MethodPost, "/")
	e.Body = "!!!not-base64"
	e.IsBase64Encoded = true

	_, err := lambdahttp.Handler(http.NotFoundHandler())(t.Context(), e)
	require.Error(t, err)
}

func TestRequestContextDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()

	var got time.Time
	_, err := lambdahttp.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Deadline()
	}))(ctx, event(http.MethodGet, "/"))
	require.NoError(t, err)
	require.True(t, deadline.Equal(got))
}

func TestResponseSetCookies(t *testing.T) {
	resp := invoke(t, func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "1"})
		http.SetCookie(w, &http.Cookie{Name: "b", Value: "2"})
	}, event(http.MethodGet, "/"))

	require.Len(t, resp.Cookies, 2)
	require.NotContains(t, resp.Headers, "Set-Cookie")
}

func TestResponseBody(t *testing.T) {
	binary := []byte{0xff, 0xd8, 0xff}
	resp := invoke(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(binary)
	}, event(http.MethodGet, "/"))

	require.True(t, resp.IsBase64Encoded)
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	require.Equal(t, binary, decoded)

	resp = invoke(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}, event(http.MethodGet, "/"))

	require.False(t, resp.IsBase64Encoded)
	require.JSONEq(t, `{"ok":true}`, resp.Body)
	require.Equal(t, "application/json", resp.Headers["Content-Type"])
}

func TestResponseStatus(t *testing.T) {
	resp := invoke(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("nope"))
	}, event(http.MethodGet, "/"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp = invoke(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }, event(http.MethodGet, "/"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp = invoke(t, func(http.ResponseWriter, *http.Request) {}, event(http.MethodGet, "/"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestResponseContentTypeSniffing(t *testing.T) {
	resp := invoke(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>hi</body></html>"))
	}, event(http.MethodGet, "/"))

	require.Equal(t, "text/html; charset=utf-8", resp.Headers["Content-Type"])
}

func TestResponseFlush(t *testing.T) {
	invoke(t, func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, http.NewResponseController(w).Flush())
	}, event(http.MethodGet, "/"))
}
