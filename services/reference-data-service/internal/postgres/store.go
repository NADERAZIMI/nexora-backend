// Package postgres is the authoritative PostgreSQL 17 persistence adapter for
// reference data. Document bytes are stored exactly as published; no cache or
// in-memory structure is authoritative.
//
//nolint:revive
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexora-exchange/nexora/services/reference-data-service/internal/reference"
)

// Store implements reference.Store against PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New opens and verifies a PostgreSQL pool.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns < 4 || cfg.MaxConns > 32 {
		cfg.MaxConns = 12
	}
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Pool exposes the pool for migration and integration tests.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping verifies database reachability.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Migrate applies or reverses embedded SQL migrations under an advisory lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, direction string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('nexora_reference_data_migrations'))`); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('nexora_reference_data_migrations'))`)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, err := migrationFiles(fsys, direction)
	if err != nil {
		return err
	}
	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM public.schema_migrations WHERE version LIKE 'reference-%'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		applied[version] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if direction == "down" {
		for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
			files[i], files[j] = files[j], files[i]
		}
	}
	for _, file := range files {
		version := "reference-" + strings.SplitN(file.name, "_", 2)[0]
		if direction == "up" && applied[version] {
			continue
		}
		if direction == "down" && !applied[version] {
			continue
		}
		sqlBytes, err := fs.ReadFile(fsys, file.name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", file.name, err)
		}
		if direction == "up" {
			if _, err := tx.Exec(ctx, `INSERT INTO public.schema_migrations (version, name) VALUES ($1,$2)`, version, file.name); err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version=$1`, version); err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

type migrationFile struct{ name string }

func migrationFiles(fsys fs.FS, direction string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	suffix := "." + direction + ".sql"
	var out []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		out = append(out, migrationFile{name: entry.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if len(out) == 0 {
		return nil, errors.New("no migrations found")
	}
	return out, nil
}

func mapPgError(err error, fallbackMessage string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case strings.Contains(pgErr.Message, "creator cannot approve"):
		return reference.NewError(403, reference.CodeSelfApproval, "a proposal creator cannot approve their own proposal", nil)
	case strings.Contains(pgErr.Message, "content is immutable"):
		return reference.Conflict(reference.CodeInvalidState, "submitted proposal content is immutable", nil)
	case strings.Contains(pgErr.Message, "append-only"):
		return reference.Conflict(reference.CodeInvalidState, "immutable records cannot be modified", nil)
	case pgErr.Code == "23505":
		return reference.Conflict(reference.CodeConflict, fallbackMessage, nil)
	default:
		return reference.Conflict(reference.CodeConflict, fallbackMessage, nil)
	}
}

func nullInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
