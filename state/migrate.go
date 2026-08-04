package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linxin2429/bili_notify/state/migrations"
	"github.com/pressly/goose/v3"
)

func runMigrations(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("creating migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("applying database migrations: %w", err)
	}
	return nil
}
