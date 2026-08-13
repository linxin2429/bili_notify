package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linxin2429/bili_notify/state/migrations"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/pressly/goose/v3"
)

func runMigrations(ctx context.Context, db *sql.DB, v *vault.Vault) error {
	if v == nil {
		return fmt.Errorf("database migration vault is required")
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations.FS,
		goose.WithGoMigrations(goose.NewGoMigration(10, &goose.GoFunc{RunTx: migrateV10(v)}, nil)),
		goose.WithGoMigrations(goose.NewGoMigration(11, &goose.GoFunc{RunTx: migrateV11(v)}, nil)),
		goose.WithGoMigrations(goose.NewGoMigration(13, &goose.GoFunc{RunTx: migrateV13}, nil)),
	)
	if err != nil {
		return fmt.Errorf("creating migration provider: %w", err)
	}
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading database migration version: %w", err)
	}
	sources := provider.ListSources()
	if len(sources) == 0 {
		return fmt.Errorf("database migrations are empty")
	}
	latest := sources[len(sources)-1].Version
	if current > latest {
		return fmt.Errorf("database migration version %d is newer than supported version %d", current, latest)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("applying database migrations: %w", err)
	}
	return nil
}
