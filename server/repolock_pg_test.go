package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/easylab-platform/easyvcs/store"
)

// pgStore opens a second CentralStore on the same Postgres DB, simulating a
// second replica. Skips when no Postgres DSN is configured.
func pgStore(t *testing.T, dsn string) *store.CentralStore {
	t.Helper()
	cs, err := store.OpenDriver(store.DriverConfig{Kind: store.KindPostgres, DSN: dsn})
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	return cs
}

// TestRepoLockerPostgresMutualExclusion proves the cross-replica per-repository
// lock serialises writes: two replicas (two Server instances on the same
// Postgres DB) cannot hold the same repo lock at once, and a release lets the
// other proceed.
func TestRepoLockerPostgresMutualExclusion(t *testing.T) {
	dsn := os.Getenv("EASYVCS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("EASYVCS_TEST_PG_DSN not set; skipping postgres integration")
	}
	csA := pgStore(t, dsn)
	defer func() { _ = csA.Close() }()
	csB := pgStore(t, dsn)
	defer func() { _ = csB.Close() }()

	a := New(csA, nil)
	b := New(csB, nil)
	if _, ok := a.repoLocks.(*pgRepoLocker); !ok {
		t.Fatalf("postgres server should use pgRepoLocker, got %T", a.repoLocks)
	}

	// A acquires the lock for repo 7; B must not be able to take it within a
	// short window (its acquisition deadline is generous, so we bound the wait
	// with a context).
	unlockA, err := a.repoLocks.lock(context.Background(), 7)
	if err != nil {
		t.Fatalf("A lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := b.repoLocks.lock(ctx, 7); err == nil {
		t.Fatal("B acquired a lock A holds (no mutual exclusion)")
	}
	// After A releases, B can take it.
	unlockA()
	unlockB, err := b.repoLocks.lock(context.Background(), 7)
	if err != nil {
		t.Fatalf("B lock after release: %v", err)
	}
	unlockB()
}

// TestRepoLockerSQLiteIsProcessLocal pins that a non-Postgres backend keeps the
// in-process mutex (single instance) and does NOT try to reach Postgres.
func TestRepoLockerSQLiteIsProcessLocal(t *testing.T) {
	s := newTestServer(t, "secret")
	if _, ok := s.repoLocks.(*localRepoLocker); !ok {
		t.Fatalf("sqlite server should use localRepoLocker, got %T", s.repoLocks)
	}
}

// TestParseMirrorSchedulerMode covers the mode/default matrix.
func TestParseMirrorSchedulerMode(t *testing.T) {
	cases := []struct {
		in, driver string
		want       MirrorSchedulerMode
		ok         bool
	}{
		{"", store.KindPostgres, MirrorSchedulerLeader, true},
		{"", store.KindSQLite, MirrorSchedulerAlways, true},
		{"leader", store.KindSQLite, MirrorSchedulerLeader, true},
		{"always", store.KindPostgres, MirrorSchedulerAlways, true},
		{"off", store.KindPostgres, MirrorSchedulerOff, true},
		{"bogus", store.KindPostgres, "", false},
	}
	for _, c := range cases {
		got, ok := ParseMirrorSchedulerMode(c.in, c.driver)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q,%q) = (%q,%v), want (%q,%v)", c.in, c.driver, got, ok, c.want, c.ok)
		}
	}
}
