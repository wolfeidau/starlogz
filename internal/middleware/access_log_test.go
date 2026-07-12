package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestAccessLogEmitsBoundedRequestEvent(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth2/{action}", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("ok"))
		require.NoError(t, err)
	})
	handler := AccessLog(logger)(mux)

	request := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?code=secret-code&state=secret-state", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Cookie", "session=secret-cookie")
	request.Header.Set("User-Agent", "secret-user-agent")
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1},
		SpanID:  trace.SpanID{1},
	})
	request = request.WithContext(trace.ContextWithSpanContext(request.Context(), spanContext))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, "http_request", event["msg"])
	require.Equal(t, "GET", event["method"])
	require.Equal(t, "/oauth2/{action}", event["route"])
	require.Equal(t, float64(http.StatusOK), event["status"])
	require.Equal(t, float64(2), event["response_bytes"])
	require.NotEmpty(t, event["request_id"])
	require.Equal(t, spanContext.TraceID().String(), event["trace_id"])
	require.Contains(t, event, "duration_ms")

	logOutput := output.String()
	for _, secret := range []string{"secret-code", "secret-state", "secret-token", "secret-cookie", "secret-user-agent"} {
		require.NotContains(t, logOutput, secret)
	}
}

func TestAccessLogUsesUnmatchedRoute(t *testing.T) {
	var output bytes.Buffer
	handler := AccessLog(slog.New(slog.NewJSONHandler(&output, nil)))(http.NewServeMux())

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/private/value?query=secret", nil))

	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, "unmatched", event["route"])
	require.NotContains(t, event, "trace_id")
	require.NotContains(t, output.String(), "private")
	require.NotContains(t, output.String(), "secret")
}

func TestAccessLogClassifiesUserAgentWithoutLoggingRawValue(t *testing.T) {
	var output bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(http.ResponseWriter, *http.Request) {})
	handler := AccessLog(slog.New(slog.NewJSONHandler(&output, nil)))(mux)
	const rawUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("User-Agent", rawUserAgent)

	handler.ServeHTTP(httptest.NewRecorder(), request)

	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, "browser", event["client_kind"])
	require.Equal(t, "chrome", event["client_family"])
	require.Equal(t, float64(126), event["client_major"])
	require.Equal(t, "macos", event["os_family"])
	require.Equal(t, "desktop", event["device_class"])
	require.NotContains(t, output.String(), rawUserAgent)
}

func TestAccessLogBoundsUnknownUserAgent(t *testing.T) {
	var output bytes.Buffer
	handler := AccessLog(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("User-Agent", "private-client/secret-value")

	handler.ServeHTTP(httptest.NewRecorder(), request)

	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, "other", event["client_kind"])
	require.Equal(t, "other", event["client_family"])
	require.Equal(t, "other", event["os_family"])
	require.Equal(t, "other", event["device_class"])
	require.NotContains(t, event, "client_major")
	require.NotContains(t, output.String(), "private-client")
	require.NotContains(t, output.String(), "secret-value")
}

func TestAccessLogDistinguishesRootCatchAll(t *testing.T) {
	var output bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	handler := AccessLog(slog.New(slog.NewJSONHandler(&output, nil)))(mux)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/scanner/path", nil))

	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, "/*", event["route"])
	require.NotContains(t, output.String(), "scanner")
}

func TestAccessLogRecordsImplicitOKWithoutBody(t *testing.T) {
	var output bytes.Buffer
	handler := AccessLog(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, float64(http.StatusOK), event["status"])
}
