package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gorm.io/gorm/clause"
)

// sqlDialect is defined in driver.go; it provides both the raw *sql.DB handle
// (for SetWAL and legacy helpers) and the GORM session used for model queries.
// CentralStore is the single-database store. It holds many repositories, each
// identified by (namespace, name). Objects are global (content-addressed and
// deduplicated); metadata tables are scoped by a repo id.
type CentralStore struct {
	d    *sqlDialect
	root string
	// openKnown/openValue cache instanceIsOpen(); openMu guards them.
	openMu    sync.Mutex
	openKnown bool
	openValue bool
}

// HomeDir returns the EasyVCS home directory (default ~/.easyvcs, overridable
// via EASYVCS_HOME).
func HomeDir() string {
	if h := os.Getenv("EASYVCS_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".easyvcs"
	}
	return filepath.Join(home, ".easyvcs")
}

// DBPath returns the path to the central database file.
func DBPath() string { return filepath.Join(HomeDir(), DefaultDBFile) }

// SetWAL enables SQLite WAL mode for safe concurrent access across processes
// (many workspaces sharing one DB), plus a busy timeout so concurrent writers
// queue instead of failing with SQLITE_BUSY. No-op for Postgres/MySQL.
func (s *CentralStore) SetWAL() error {
	if !s.d.isSQLite() {
		return nil
	}
	if err := s.d.gdb.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		return err
	}
	if err := s.d.gdb.Exec("PRAGMA busy_timeout=10000").Error; err != nil {
		return err
	}
	return nil
}

// cleanupStaleColumns drops columns that a previous schema shipped but the
// current models no longer declare. Without this, GORM's SQLite migrator
// rebuilds the table (create `<table>__temp`) and the column-mapping rewrite
// can violate a NOT NULL constraint while copying rows — a hard startup
// failure on any pre-bookmark DB. We drop them with native DDL first (out of
// band, not via the migrator) so AutoMigrate sees a clean target.
//
// Dropped columns are semantically obsolete: the "bookmark" concept was
// replaced by "branch"; the live value lives in default_branch / id.
func (s *CentralStore) cleanupStaleColumns() error {
	if !s.d.isSQLite() {
		return nil
	}
	// Each entry: (table, staleColumn). DROP COLUMN requires the column to
	// exist; guard on PRAGMA table_info so a fresh DB (column absent) is a
	// no-op and an already-migrated DB is idempotent.
	stale := [][2]string{
		{"repositories", "default_bookmark"},
		{"workspaces", "bookmark"},
		// Revision Requests were converged to snapshot-only: the source kind and
		// the source revision id are gone. Drop them from a pre-existing DB.
		{"revision_requests", "source_kind"},
		{"revision_requests", "source_revision_id"},
	}
	for _, c := range stale {
		table, col := c[0], c[1]
		var exists bool
		if err := s.d.gdb.Raw(
			"SELECT EXISTS (SELECT 1 FROM pragma_table_info(?) WHERE name = ?)",
			table, col,
		).Scan(&exists).Error; err != nil {
			return err
		}
		if !exists {
			continue
		}
		// Column exists in this table; drop it. Wrapped so a concurrent or
		// already-migrated DB that races with us still converges.
		if err := s.d.gdb.Exec("ALTER TABLE " + table + " DROP COLUMN " + col).Error; err != nil {
			// Some SQLite builds (pre-3.35) can't DROP COLUMN; shrinking the
			// scope: if it fails, that's a hard upgrade requirement. Surface it.
			return fmt.Errorf("drop stale column %s.%s: %w", table, col, err)
		}
	}
	return nil
}

// Init creates the schema via GORM AutoMigrate, generating portable DDL
// (sqlite / postgres / mysql). Idempotent.
//
// On Postgres two replicas may boot at the SAME time; concurrent AutoMigrate
// DDL races (e.g. both CREATE the same index → "duplicate key value violates
// unique constraint pg_class_relname_nsp_index"). A migration advisory lock
// serialises schema setup across replicas: the first to grab it migrates, the
// rest wait and then find the schema already present (idempotent). SQLite/MySQL
// are single-writer here and skip the lock.
func (s *CentralStore) Init() error {
	if s.d.kind == KindPostgres {
		unlock, err := s.lockMigration()
		if err != nil {
			return err
		}
		defer unlock()
	}
	if err := s.cleanupStaleColumns(); err != nil {
		return err
	}
	if err := s.d.gdb.AutoMigrate(allModels()...); err != nil {
		return err
	}
	if err := s.migrateOwnership(); err != nil {
		return err
	}
	return nil
}

// migrationLockKey is the advisory-lock key serialising schema migration across
// replicas. It sits just above mirrorLeaderKey, far from any repo id (positive).
const migrationLockKey = int64(-1<<63) + 1

// lockMigration takes a session advisory lock on a dedicated connection so only
// one replica runs AutoMigrate at a time. The lock is released by unlock (or
// when the connection closes).
func (s *CentralStore) lockMigration() (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := s.d.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration lock: conn: %w", err)
	}
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockKey).Scan(&got); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("migration lock: %w", err)
		}
		if got {
			break
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return nil, fmt.Errorf("migration lock: timed out: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey)
		_ = conn.Close()
	}, nil
}

// migrateOwnership is the one-time ownership bootstrap. User IS the ownership
// boundary, so this rebuilds the repository unique index onto the globally
// unique (namespace, name) pair (AutoMigrate cannot be trusted to re-shape an
// existing same-named index). Idempotent on every backend.
//
// The drop uses EXPLICIT DDL, not Migrator().DropIndex: the Postgres driver's
// DropIndex emits `DROP INDEX CURRENT_SCHEMA()."idx_repo"` when the statement
// has no table schema, which Postgres rejects (syntax error at "CURRENT_SCHEMA").
// `DROP INDEX IF EXISTS <name>` works on both Postgres and SQLite (SQLite index
// names are global; Postgres resolves the unqualified name on the search_path).
func (s *CentralStore) migrateOwnership() error {
	if err := s.dropLegacyRepoIndex(); err != nil {
		return err
	}
	return s.d.gdb.Migrator().CreateIndex(&repoRow{}, "idx_repo")
}

// dropLegacyRepoIndex removes idx_repo if it exists, in a dialect-safe way.
func (s *CentralStore) dropLegacyRepoIndex() error {
	switch s.d.kind {
	case KindPostgres, KindSQLite:
		if err := s.d.gdb.Exec("DROP INDEX IF EXISTS idx_repo").Error; err != nil {
			return fmt.Errorf("drop legacy repo index: %w", err)
		}
	case KindMySQL:
		if s.d.gdb.Migrator().HasIndex(&repoRow{}, "idx_repo") {
			if err := s.d.gdb.Exec("DROP INDEX idx_repo ON repositories").Error; err != nil {
				return fmt.Errorf("drop legacy repo index: %w", err)
			}
		}
	default:
		return fmt.Errorf("migrateOwnership: unsupported driver %q", s.d.kind)
	}
	return nil
}

// ensureNamespaceRow inserts a namespace owned by owner when absent.
func (s *CentralStore) ensureNamespaceRow(owner int64, name string) error {
	if name == "" || owner == 0 {
		return nil
	}
	row := &namespaceRow{OwnerUserID: owner, Name: name, Created: time.Now().UTC().UnixMilli()}
	return s.d.gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

// Close closes the underlying database.
func (s *CentralStore) Close() error { return s.d.db.Close() }
