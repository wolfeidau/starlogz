package commands

import (
	"context"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/wolfeidau/starlogz/internal/lambdahttp"
)

type LambdaCmd struct {
	ServerFlags `embed:""`
}

func (c *LambdaCmd) Run(ctx context.Context, globals *Globals) error {
	srv, cleanup, err := c.newServer(ctx, globals, 0)
	if err != nil {
		return err
	}
	defer cleanup()

	// Without an extension Lambda sends no SIGTERM, so deferred Sentry/OTel flushes in main would never run.
	go lambda.StartWithOptions(lambdahttp.Handler(srv.Handler()), lambda.WithContext(ctx), lambda.WithEnableSIGTERM())

	<-ctx.Done()
	return nil
}
