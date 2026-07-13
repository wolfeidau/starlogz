package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/medama-io/go-useragent"
	"github.com/medama-io/go-useragent/agents"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"go.opentelemetry.io/otel/trace"
)

const otherClassification = "other"

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rw *responseWriter) WriteHeader(status int) {
	if rw.status != 0 {
		return
	}
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += int64(n)
	return n, err
}

// Unwrap allows http.ResponseController to reach the underlying ResponseWriter.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// AccessLog returns middleware that logs one bounded event per request. It seeds
// a request_id into the context logger
// so all log lines within a request share a correlation field.
// Place inside otelhttp so trace context is available.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	parser := useragent.NewParser()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := uuid.New().String()
			reqLogger := logger.With(
				slog.String("request_id", requestID),
			)
			ctx := ctxlog.WithRequestID(r.Context(), requestID)
			r = r.WithContext(ctxlog.WithLogger(ctx, reqLogger))

			start := time.Now()
			rw := &responseWriter{ResponseWriter: w}

			next.ServeHTTP(rw, r)
			if rw.status == 0 {
				rw.status = http.StatusOK
			}

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("route", routePattern(r)),
				slog.Int("status", rw.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.Int64("response_bytes", rw.bytes),
			}
			spanContext := trace.SpanContextFromContext(r.Context())
			if spanContext.IsValid() {
				attrs = append(attrs, slog.String("trace_id", spanContext.TraceID().String()))
			}
			attrs = append(attrs, userAgentAttrs(parser.Parse(r.UserAgent()))...)

			reqLogger.LogAttrs(r.Context(), slog.LevelInfo, "http_request", attrs...)
		})
	}
}

func routePattern(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	parts := strings.Fields(r.Pattern)
	pattern := parts[len(parts)-1]
	if pattern == "/" && r.URL.Path != "/" {
		return "/*"
	}
	return pattern
}

func userAgentAttrs(ua useragent.UserAgent) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("client_kind", clientKind(ua)),
		slog.String("client_family", browserFamily(ua.Browser())),
		slog.String("os_family", osFamily(ua.OS())),
		slog.String("device_class", deviceClass(ua.Device())),
	}
	if major, err := strconv.Atoi(ua.BrowserVersionMajor()); err == nil && major >= 0 && major <= 999 {
		attrs = append(attrs, slog.Int("client_major", major))
	}
	return attrs
}

func clientKind(ua useragent.UserAgent) string {
	if ua.IsBot() {
		return "bot"
	}
	if ua.Browser() != "" {
		return "browser"
	}
	return otherClassification
}

// Keep log values stable and bounded when the parser adds or renames browsers.
func browserFamily(browser agents.Browser) string {
	switch browser {
	case agents.BrowserAndroid:
		return "android"
	case agents.BrowserChrome:
		return "chrome"
	case agents.BrowserEdge:
		return "edge"
	case agents.BrowserFirefox:
		return "firefox"
	case agents.BrowserIE:
		return "ie"
	case agents.BrowserOpera:
		return "opera"
	case agents.BrowserOperaMini:
		return "opera_mini"
	case agents.BrowserSafari:
		return "safari"
	case agents.BrowserVivaldi:
		return "vivaldi"
	case agents.BrowserSilk:
		return "silk"
	case agents.BrowserSamsung:
		return "samsung"
	case agents.BrowserFalkon:
		return "falkon"
	case agents.BrowserNintendo:
		return "nintendo"
	case agents.BrowserYandex:
		return "yandex"
	default:
		return otherClassification
	}
}

// Keep log values stable and bounded when the parser adds or renames operating systems.
func osFamily(os agents.OS) string {
	switch os {
	case agents.OSAndroid:
		return "android"
	case agents.OSChromeOS:
		return "chromeos"
	case agents.OSIOS:
		return "ios"
	case agents.OSLinux:
		return "linux"
	case agents.OSFreeBSD:
		return "freebsd"
	case agents.OSOpenBSD:
		return "openbsd"
	case agents.OSMacOS:
		return "macos"
	case agents.OSWindows:
		return "windows"
	default:
		return otherClassification
	}
}

func deviceClass(device agents.Device) string {
	switch device {
	case agents.DeviceDesktop:
		return "desktop"
	case agents.DeviceMobile:
		return "mobile"
	case agents.DeviceTablet:
		return "tablet"
	case agents.DeviceTV:
		return "tv"
	case agents.DeviceBot:
		return "bot"
	default:
		return otherClassification
	}
}
