//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yshengliao/gortexa/internal/config"
	"github.com/yshengliao/gortexa/internal/storage"
	"github.com/yshengliao/gortexa/internal/storage/db"
)

// TestPgBouncerPoolCRUD proves the pool's PgBouncer-safety claim against a
// real PgBouncer in transaction-pooling mode (deploy/docker-compose.yaml):
// QueryExecModeExec with disabled statement/description caches must survive
// pooled-transaction connection switching — exactly where server-side named
// prepared statements break. It also applies db/migrations/0001_init.sql to a
// real PostgreSQL, catching migration/sqlc schema drift.
//
// Set PGBOUNCER_DSN to force the test (a connection failure then fails rather
// than skips); without it, an unreachable local broker skips.
func TestPgBouncerPoolCRUD(t *testing.T) {
	dsn := os.Getenv("PGBOUNCER_DSN")
	forced := dsn != ""
	if dsn == "" {
		dsn = "postgres://postgres@127.0.0.1:6432/gortexa?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := storage.NewPool(ctx, config.DBConfig{DSN: config.Secret(dsn), MaxConns: 4}, nil)
	if err != nil {
		t.Fatal(err) // DSN parse failure is a real bug, not an absent broker
	}
	t.Cleanup(pool.Close)

	// NewPool connects lazily, so reachability must be probed explicitly for
	// the skip-vs-fail decision.
	if err := pool.Ping(ctx); err != nil {
		if forced {
			t.Fatalf("pgbouncer not reachable at %s: %v", dsn, err)
		}
		t.Skipf("pgbouncer not reachable: %v (start it with: docker compose -f deploy/docker-compose.yaml up -d postgres pgbouncer)", err)
	}

	// Apply the real migration to a clean slate: schema drift between
	// db/migrations and the sqlc-generated queries fails here.
	migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", "0001_init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS resources CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	q := db.New(pool)

	// Loop enough times to cycle through the pool and PgBouncer's
	// transaction-mode connection multiplexing.
	for i := range 25 {
		id := fmt.Sprintf("it-%02d", i)
		created, err := q.CreateResource(ctx, db.CreateResourceParams{ID: id, Name: "n" + id, Owner: "it", Status: "STATUS_ACTIVE"})
		if err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		got, err := q.GetResource(ctx, created.ID)
		if err != nil || got.Name != "n"+id {
			t.Fatalf("get %s: %+v, %v", id, got, err)
		}
	}

	listed, err := q.ListResources(ctx, db.ListResourcesParams{Owner: "it", PageLimit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 25 {
		t.Fatalf("list returned %d rows, want 25", len(listed))
	}

	upd, err := q.UpdateResource(ctx, db.UpdateResourceParams{ID: "it-00", Name: "renamed", Owner: "it", Status: "STATUS_INACTIVE"})
	if err != nil || upd.Name != "renamed" {
		t.Fatalf("update: %+v, %v", upd, err)
	}
	if err := q.DeleteResource(ctx, "it-01"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := q.GetResource(ctx, "it-01"); err == nil {
		t.Fatal("get after delete should fail")
	}
}
