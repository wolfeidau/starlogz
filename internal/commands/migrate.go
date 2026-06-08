package commands

import (
	"context"
	"fmt"

	"github.com/wolfeidau/starlogz/internal/store"
	"github.com/wolfeidau/starlogz/internal/store/postgres"
)

type MigrateCmd struct {
	DatabaseURL string `help:"PostgreSQL connection string." env:"DATABASE_URL" required:""`
}

func (c *MigrateCmd) Run(ctx context.Context, globals *Globals) error {
	st, err := postgres.New(ctx, c.DatabaseURL, store.NewEncryptor([32]byte{}))
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer st.Close()

	return st.Migrate(ctx, globals.Logger)
}
