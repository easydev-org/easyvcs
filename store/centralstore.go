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
// (for SetWAL and low-level helpers) and the GORM session used for queries.
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
	// idxReady caches, per namespace, whether the revision_path_dirs index has
	// ever been populated. Rows are only ever ADDED, so once a namespace has a
	// row the lazy-backfill check is unnecessary; this avoids a COUNT on every
	// RevisionsTouching call.
	idxMu    sync.Mutex
	idxReady map[int64]bool
	// objCache is a bounded LRU of decoded objects (content-addressed and
	// immutable, so it never goes stale). Sized by WithObjectCache.
	objCache *objectCache
	// snapCache is a bounded LRU of SNAPSHOTS, keyed by their content-addressed
	// hash (immutable, so it never goes stale). It absorbs the repeated snapshot
	// lookups of log / browse / history.
	//
	// NOTE: there is deliberately NO revision cache. A revision is keyed by its
	// STABLE id but its `hash` MUTATES on amend/rewrite, so an id->hash cache
	// would serve a stale hash when the row is rewritten by another store
	// instance / replica.
	snapCache *lru[*Snapshot]
	// idxQueue defers the path-index write off the commit path. nil = synchronous
	// (the safe default; the server/CLI opt in via WithAsyncIndex).
	idxQueue *indexQueue
}

// WithAsyncIndex moves the path-index write (revision_path_dirs) off the commit
// transaction onto a background worker. RevisionsTouching flushes the namespace
// first, so reads still see their own writes. Returns the store for chaining.
func (s *CentralStore) WithAsyncIndex() *CentralStore {
	if s.idxQueue == nil {
		s.idxQueue = newIndexQueue()
		go s.indexWorker()
	}
	return s
}

// DefaultObjectCacheEntries is the default LRU capacity (number of objects).
const DefaultObjectCacheEntries = 4096

// WithObjectCache sets the object-cache capacity (0 disables). It returns the
// store for chaining. Call before serving.
func (s *CentralStore) WithObjectCache(entries int) *CentralStore {
	s.objCache = newObjectCache(entries)
	s.snapCache = newLRU[*Snapshot](entries)
	return s
}

// ObjectCacheStats returns (hits, misses) for the object cache.
func (s *CentralStore) ObjectCacheStats() (uint64, uint64) { return s.objCache.stats() }

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

// Init creates the schema via GORM AutoMigrate, generating portable DDL
// (sqlite / postgres / mysql). Idempotent.
//
// On Postgres several replicas may boot at the SAME time; concurrent AutoMigrate
// DDL races (e.g. both CREATE the same index → "duplicate key value violates
// unique constraint pg_class_relname_nsp_index"). A schema advisory lock
// serialises setup across replicas: the first to grab it creates the schema, the
// rest wait and then find it already present. SQLite/MySQL are single-writer
// here and skip the lock.
//
// There is NO data migration: the schema is created once, fresh. An existing
// database from an older schema is not upgraded in place.
func (s *CentralStore) Init() error {
	if s.d.kind == KindPostgres {
		unlock, err := s.lockMigration()
		if err != nil {
			return err
		}
		defer unlock()
	}
	return s.d.gdb.AutoMigrate(allModels()...)
}

// migrationLockKey is the advisory-lock key serialising SCHEMA CREATION across
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

// indexReady reports (with a process-local cache) whether the path index for a
// namespace has been checked/populated.
func (s *CentralStore) indexReady(namespaceID int64) bool {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	return s.idxReady[namespaceID]
}

// markIndexReady records that a namespace's path index is present.
func (s *CentralStore) markIndexReady(namespaceID int64) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.idxReady == nil {
		s.idxReady = map[int64]bool{}
	}
	s.idxReady[namespaceID] = true
}
