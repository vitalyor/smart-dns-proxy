// Package store owns the PostgreSQL connection pool, migrations and typed
// access helpers. Every mutable entity carries a `version` column used for
// optimistic locking through If-Match.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:migrations
var migrationsFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on optimistic-locking or uniqueness failures.
var ErrConflict = errors.New("conflict")

// DB wraps the pool.
type DB struct{ *pgxpool.Pool }

// Open connects with sane pool defaults and verifies connectivity.
func Open(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(c); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{pool}, nil
}

// Migrate applies every embedded migration that has not run yet.
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		var exists bool
		if err := db.QueryRow(ctx, `SELECT true FROM schema_migrations WHERE name=$1`, n).Scan(&exists); err == nil {
			continue
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		body, err := migrationsFS.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", n, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, n); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// One scans a single row into T, mapping columns by `db` struct tag.
func One[T any](ctx context.Context, db *DB, sql string, args ...any) (T, error) {
	var zero T
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return zero, wrap(err)
	}
	v, err := pgx.CollectOneRow(rows, scanStruct[T])
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrNotFound
	}
	return v, wrap(err)
}

// Many scans all rows into []T.
func Many[T any](ctx context.Context, db *DB, sql string, args ...any) ([]T, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap(err)
	}
	v, err := pgx.CollectRows(rows, scanStruct[T])
	if v == nil {
		v = []T{}
	}
	return v, wrap(err)
}

// scanStruct maps result columns onto struct fields by `db` tag. Unlike pgx's
// built-in mappers it tolerates columns the struct does not declare, so a
// `SELECT *` keeps working when a migration adds a column, and embedded
// structs are flattened so joined queries can reuse entity types.
func scanStruct[T any](row pgx.CollectableRow) (T, error) {
	var out T
	fields := row.FieldDescriptions()
	targets := make([]any, len(fields))
	byName := map[string]reflect.Value{}
	collect(reflect.ValueOf(&out).Elem(), byName)
	var discard any
	for i, f := range fields {
		if v, ok := byName[f.Name]; ok {
			targets[i] = v.Addr().Interface()
			continue
		}
		targets[i] = &discard
	}
	err := row.Scan(targets...)
	return out, err
}

func collect(v reflect.Value, out map[string]reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("db") == "" {
			collect(v.Field(i), out)
			continue
		}
		name := f.Tag.Get("db")
		if name == "" || name == "-" {
			continue
		}
		if _, exists := out[name]; !exists {
			out[name] = v.Field(i)
		}
	}
}

// Value scans a single scalar.
func Value[T any](ctx context.Context, db *DB, sql string, args ...any) (T, error) {
	var v T
	err := db.QueryRow(ctx, sql, args...).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, wrap(err)
}

// Exec runs a statement and reports the affected row count.
func (db *DB) ExecN(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := db.Exec(ctx, sql, args...)
	if err != nil {
		return 0, wrap(err)
	}
	return tag.RowsAffected(), nil
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", ErrConflict, pg.Detail)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", ErrConflict, pg.Detail)
		}
	}
	return err
}
