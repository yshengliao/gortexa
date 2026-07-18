// Package storage builds a PgBouncer-safe pgx connection pool and a query
// tracer that emits OTel DB spans. It pairs with sqlc-generated, type-safe
// queries under internal/storage/db.
package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperr "github.com/yshengliao/gortexa/apperr"
	"github.com/yshengliao/gortexa/config"
)

// BuildPoolConfig parses the DSN and applies PgBouncer transaction-mode safety:
// the simple exec protocol (no server-side named prepared statements, which
// break across pooled connections) and disabled statement/description caches.
func BuildPoolConfig(cfg config.DBConfig, tracer pgx.QueryTracer) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN.Reveal())
	if err != nil {
		return nil, apperr.Wrap(apperr.CatInvalidArgument, "parse db dsn", err)
	}
	pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pc.ConnConfig.StatementCacheCapacity = 0
	pc.ConnConfig.DescriptionCacheCapacity = 0
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if t, ok := tracer.(*DBTracer); ok {
		tracer = t.WithServerAddress(pc.ConnConfig.Host)
	}
	if tracer != nil {
		pc.ConnConfig.Tracer = tracer
	}
	return pc, nil
}

// NewPool builds and connects a PgBouncer-safe pgx pool.
func NewPool(ctx context.Context, cfg config.DBConfig, tracer pgx.QueryTracer) (*pgxpool.Pool, error) {
	pc, err := BuildPoolConfig(cfg, tracer)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, apperr.Wrap(apperr.CatUnavailable, "connect db", err)
	}
	return pool, nil
}
