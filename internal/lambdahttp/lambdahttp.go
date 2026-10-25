package lambdahttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/events"
)

// Handler adapts an http.Handler to API Gateway HTTP API (payload v2) events.
func Handler(h http.Handler) func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return func(ctx context.Context, e events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
		r, err := newRequest(ctx, e)
		if err != nil {
			return events.APIGatewayV2HTTPResponse{}, err
		}
		w := &responseWriter{header: http.Header{}}
		h.ServeHTTP(w, r)
		return w.response(), nil
	}
}

func newRequest(ctx context.Context, e events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	target := e.RawPath
	if e.RawQueryString != "" {
		target += "?" + e.RawQueryString
	}
	// Parse like net/http's server so handlers see the same r.URL (path and query only) as the local listener.
	u, err := url.ParseRequestURI(target)
	if err != nil {
		return nil, fmt.Errorf("failed to parse request url: %w", err)
	}

	host := e.Headers["host"]
	if host == "" {
		host = e.RequestContext.DomainName
	}

	body := []byte(e.Body)
	if e.IsBase64Encoded {
		body, err = base64.StdEncoding.DecodeString(e.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to decode request body: %w", err)
		}
	}

	r, err := http.NewRequestWithContext(ctx, e.RequestContext.HTTP.Method, "https://"+host+target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	r.URL = u
	r.Host = host
	r.RequestURI = target
	r.ContentLength = int64(len(body))
	r.RemoteAddr = net.JoinHostPort(e.RequestContext.HTTP.SourceIP, "0")

	r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/1.1", 1, 1
	if major, minor, ok := http.ParseHTTPVersion(e.RequestContext.HTTP.Protocol); ok {
		r.Proto, r.ProtoMajor, r.ProtoMinor = e.RequestContext.HTTP.Protocol, major, minor
	}

	for k, v := range e.Headers {
		r.Header.Add(k, v)
	}
	if len(e.Cookies) > 0 {
		r.Header.Set("Cookie", strings.Join(e.Cookies, "; "))
	}
	return r, nil
}

type responseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *responseWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}

// Flush is a no-op because the response is buffered until the handler returns.
func (w *responseWriter) Flush() {}

func (w *responseWriter) response() events.APIGatewayV2HTTPResponse {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	b := w.body.Bytes()
	if w.header.Get("Content-Type") == "" && len(b) > 0 {
		w.header.Set("Content-Type", http.DetectContentType(b))
	}

	resp := events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    make(map[string]string, len(w.header)),
	}
	for k, vs := range w.header {
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			resp.Cookies = append(resp.Cookies, vs...)
			continue
		}
		resp.Headers[k] = strings.Join(vs, ", ")
	}

	if utf8.Valid(b) {
		resp.Body = string(b)
	} else {
		resp.Body = base64.StdEncoding.EncodeToString(b)
		resp.IsBase64Encoded = true
	}
	return resp
}
