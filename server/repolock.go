package server

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/easylab-platform/easyvcs/store"
)

// repoLocker serialises writes to a single repository for the duration of one
// logical operation (a push, an RR merge, a commit, a branch sync).
//
// On SQLite a single process is the only writer, so an in-process map of
// mutexes is sufficient. On Postgres two or more replicas may serve the same
// repository concurrently, so the lock must be held in the DATABASE: a Postgres
// advisory lock is taken on a dedicated connection and released when the
// operation finishes (or the replica dies and the connection drops).
//
// The lock is keyed by the repository id (stable across replicas), so unrelated
// repositories still proceed in parallel.
type repoLocker interface {
	// lock acquires the repository lock, returning a function that releases it.
	// It blocks until the lock is held (or the acquisition deadline passes) and
	// returns an error only on an infrastructure failure.
	lock(ctx context.Context, repoID int64) (func(), error)
}

// newRepoLocker picks the implementation for the store's backend. Postgres gets
// the cross-replica advisory lock; every other backend (sqlite/mysql) keeps the
// process-local behaviour.
func newRepoLocker(cs *store.CentralStore) repoLocker {
	if cs.DriverKind() == store.KindPostgres {
		return &pgRepoLocker{db: cs.SQLDB()}
	}
	return &localRepoLocker{}
}

// ---- SQLite / single-instance: process-local mutexes ----

type localRepoLocker struct {
	locks sync.Map // int64 (repo id) -> *sync.Mutex
}

func (l *localRepoLocker) lock(_ context.Context, repoID int64) (func(), error) {
	v, _ := l.locks.LoadOrStore(repoID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock, nil
}

// ---- Postgres / multi-replica: advisory lock on a dedicated connection ----

type pgRepoLocker struct {
	db *sql.DB
}

// repoLockAcquireTimeout bounds how long a replica waits for the repository
// lock before giving up. It is generous because the lock is held for the whole
// write (a large push can take minutes); a crashed holder's lock is released by
// Postgres when its connection drops, so the wait is finite.
const repoLockAcquireTimeout = 5 * time.Minute

func (l *pgRepoLocker) lock(ctx context.Context, repoID int64) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, repoLockAcquireTimeout)
	defer cancel()
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo lock: acquire conn: %w", err)
	}
	key := repoLockKey(repoID)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("repo lock: %w", err)
		}
		if got {
			break
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return nil, fmt.Errorf("repo lock: timed out waiting for repo %d: %w", repoID, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	return func() {
		// Release on a fresh background context: the caller's ctx may already be
		// cancelled when the operation ends.
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", key)
		_ = conn.Close()
	}, nil
}

// repoLockKey derives the 64-bit advisory-lock key for a repository. Repo ids
// are positive, so they never collide with the scheduler's leader key (the
// most-negative int64; see mirrorLeaderKey).
func repoLockKey(repoID int64) int64 { return repoID }
