// Command migrate applies the versioned forward and reverse migrations. The
// --validate mode applies every migration, reverses all of them and applies
// them again, exiting non-zero on any failure.
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/golang-migrate/migrate/v4"
	pgmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	validate := flag.Bool("validate", false, "apply, reverse and re-apply every migration")
	direction := flag.String("direction", "up", "migration direction: up or down")
	path := flag.String("path", "file://migrations", "migration source directory")
	flag.Parse()

	if err := run(*validate, *direction, *path); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(validate bool, direction, path string) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	runner, closeRunner, err := open(dsn, path)
	if err != nil {
		return err
	}
	defer closeRunner()

	if validate {
		if err := migrateUp(runner); err != nil {
			return err
		}
		if err := runner.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("reversing migrations: %w", err)
		}
		return migrateUp(runner)
	}

	switch direction {
	case "up":
		return migrateUp(runner)
	case "down":
		if err := runner.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("reversing migrations: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown direction %q", direction)
	}
}

func migrateUp(runner *migrate.Migrate) error {
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

func open(dsn, path string) (*migrate.Migrate, func(), error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, err
	}
	driver, err := pgmigrate.WithInstance(db, &pgmigrate.Config{})
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	runner, err := migrate.NewWithDatabaseInstance(path, "postgres", driver)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return runner, func() {
		_, _ = runner.Close()
		_ = db.Close()
	}, nil
}
