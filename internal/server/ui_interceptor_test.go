package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
)

func runUIInterceptor(t *testing.T, handlerErr error) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	ctx := ctxlog.WithLogger(t.Context(), slog.New(slog.NewTextHandler(&buf, nil)))
	next := func(context.Context, connect.Spec, connect.ServerStream) error { return handlerErr }
	err := uiErrorLogInterceptor()(next)(ctx, connect.Spec{Procedure: "/starlogz.v1.UIService/ListTags"}, nil)
	return buf.String(), err
}

func TestUIErrorLogInterceptorLogsInternalCause(t *testing.T) {
	cause := errors.New("database exploded")
	want := connect.NewError(connect.CodeInternal, "internal error").WithCause(cause)

	logs, err := runUIInterceptor(t, want)

	require.Same(t, want, err)
	require.Contains(t, logs, "ui rpc failed")
	require.Contains(t, logs, "database exploded")
	require.Contains(t, logs, "/starlogz.v1.UIService/ListTags")
	require.Contains(t, logs, "internal")
}

func TestUIErrorLogInterceptorLogsUnknown(t *testing.T) {
	want := errors.New("plain failure")

	logs, err := runUIInterceptor(t, want)

	require.Same(t, want, err)
	require.Contains(t, logs, "plain failure")
}

func TestUIErrorLogInterceptorSkipsClientErrors(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeNotFound, connect.CodeInvalidArgument} {
		want := connect.NewError(code, "client problem")

		logs, err := runUIInterceptor(t, want)

		require.Same(t, want, err)
		require.Empty(t, logs)
	}
}
