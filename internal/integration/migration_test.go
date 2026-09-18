//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	pgmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// C10/C170 - forward and reverse migrations succeed and can be re-applied in
// a clean database.
func TestMigrationReversal(t *testing.T) {
	ctx := context.Background()
	database := "migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{database}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatalf("creating migration database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
	})
	dsn := dsnForDatabase(t, database)

	assertWallets := func(want bool) {
		t.Helper()
		connection, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("connecting to migration database: %v", err)
		}
		defer connection.Close(ctx)
		var exists bool
		if err := connection.QueryRow(ctx, `SELECT to_regclass('public.wallets') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatalf("checking wallets table: %v", err)
		}
		if exists != want {
			t.Fatalf("wallets table exists = %v, want %v", exists, want)
		}
	}

	if err := applyMigrations(dsn); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}
	assertWallets(true)
	if err := reverseMigrations(t, dsn); err != nil {
		t.Fatalf("reversing migrations: %v", err)
	}
	assertWallets(false)
	if err := applyMigrations(dsn); err != nil {
		t.Fatalf("re-applying migrations: %v", err)
	}
	assertWallets(true)
}

func dsnForDatabase(t *testing.T, database string) string {
	t.Helper()
	parsed, err := url.Parse(testDSN)
	if err != nil {
		t.Fatalf("parsing test dsn: %v", err)
	}
	parsed.Path = "/" + database
	return parsed.String()
}

func reverseMigrations(t *testing.T, dsn string) error {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	driver, err := pgmigrate.WithInstance(db, &pgmigrate.Config{})
	if err != nil {
		return err
	}
	runner, err := migrate.NewWithDatabaseInstance(migrationsDir(), "postgres", driver)
	if err != nil {
		return err
	}
	if err := runner.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
