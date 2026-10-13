package store

import (
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
)

// TestPostgresInitIdempotent pins the Postgres startup fix: Init() must succeed
// on a fresh Postgres DB and be idempotent on a second open. The legacy
// migrateOwnership step rebuilt the repository unique index, and its
// Migrator().DropIndex generated invalid DDL on Postgres ("syntax error at or
// near CURRENT_SCHEMA"); the fix uses explicit `DROP INDEX IF EXISTS`. Skipped
// when no Postgres DSN is provided.
func TestPostgresInitIdempotent(t *testing.T) {
	dsn := os.Getenv("EASYVCS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("EASYVCS_TEST_PG_DSN not set; skipping postgres integration")
	}
	// First open: schema + migrateOwnership.
	cs, err := OpenDriver(DriverConfig{Kind: KindPostgres, DSN: dsn})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	// Second open on the SAME DB: migrateOwnership must be idempotent (it drops
	// and recreates idx_repo). Before the fix this is where Postgres failed.
	cs2, err := OpenDriver(DriverConfig{Kind: KindPostgres, DSN: dsn})
	if err != nil {
		_ = cs.Close()
		t.Fatalf("second open (idempotent Init): %v", err)
	}
	_ = cs2.Close()
	// A repo/namespace CRUD round-trip on the migrated schema.
	roundTrip(t, cs)
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresConcurrentInit boots several replicas against the SAME fresh
// Postgres DB at once. Without the migration advisory lock their concurrent
// AutoMigrate DDL races ("duplicate key value violates unique constraint
// pg_class_relname_nsp_index"); with it, all succeed. Skipped without a DSN.
func TestPostgresConcurrentInit(t *testing.T) {
	dsn := os.Getenv("EASYVCS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("EASYVCS_TEST_PG_DSN not set; skipping postgres integration")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("cannot open admin conn: %v", err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec("DROP SCHEMA IF EXISTS public CASCADE"); err != nil {
		t.Skipf("cannot reset schema: %v", err)
	}
	if _, err := admin.Exec("CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}

	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			cs, err := OpenDriver(DriverConfig{Kind: KindPostgres, DSN: dsn})
			if err == nil {
				_ = cs.Close()
			}
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Init failed: %v", err)
		}
	}
}
