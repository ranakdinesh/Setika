package coredb

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ranakdinesh/setika/sql/migrations"
)

// RunMigrations applies Setika core DB migrations that do not belong to a product module.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("core db migrations: database pool is nil")
	}
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS platform;
		CREATE TABLE IF NOT EXISTS platform.schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`); err != nil {
		return fmt.Errorf("core db migrations setup: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("core db migrations read: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		version := strings.TrimSuffix(name, ".sql")
		applied, err := migrationApplied(ctx, pool, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		sqlBytes, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read core db migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, pool, version, string(sqlBytes)); err != nil {
			return err
		}
	}
	return nil
}

func migrationApplied(ctx context.Context, pool *pgxpool.Pool, version string) (bool, error) {
	var applied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
		return false, fmt.Errorf("check core db migration %s: %w", version, err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, version, statement string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin core db migration %s: %w", version, err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, statement); err != nil {
		return fmt.Errorf("apply core db migration %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("record core db migration %s: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit core db migration %s: %w", version, err)
	}
	return nil
}
