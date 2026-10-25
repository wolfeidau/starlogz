package server

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect/v2"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
)

// uiErrorLogInterceptor logs server-side failures whose causes are never sent to clients.
func uiErrorLogInterceptor() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			err := next(ctx, spec, stream)
			if err == nil {
				return nil
			}
			code := connect.CodeOf(err)
			if code != connect.CodeInternal && code != connect.CodeUnknown {
				return err
			}
			// connect.Error.Error() omits the cause, so unwrap to log it.
			cause := err
			if inner := errors.Unwrap(err); inner != nil {
				cause = inner
			}
			ctxlog.LoggerFrom(ctx).ErrorContext(ctx, "ui rpc failed",
				slog.String("procedure", spec.Procedure),
				slog.String("code", code.String()),
				slog.Any("error", cause),
			)
			return err
		}
	}
}
