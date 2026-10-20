// Package store holds all Postgres/TimescaleDB access.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	Pool *pgxpool.Pool
	read pgx.Tx
}

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// EnableAPIRole lets the read-only susq_api role log in with password.
func (s *Store) EnableAPIRole(ctx context.Context, password string) error {
	var stmt string
	if err := s.Pool.QueryRow(ctx, `SELECT format('ALTER ROLE susq_api WITH LOGIN PASSWORD %L', $1::text)`,
		password).Scan(&stmt); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, stmt)
	return err
}

// Migrate applies embedded migrations in filename order, each in its own
// transaction.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		var exists bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, base).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, base); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// reader routes a scoring snapshot's reads through one repeatable-read view.
// Writes always use the pool and are performed after that read transaction ends.
type readDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) reader() readDB {
	if s.read != nil {
		return s.read
	}
	return s.Pool
}
func (s *Store) ReadSnapshot(ctx context.Context, fn func(*Store, time.Time) error) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&at); err != nil {
		return err
	}
	if err := fn(&Store{Pool: s.Pool, read: tx}, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
