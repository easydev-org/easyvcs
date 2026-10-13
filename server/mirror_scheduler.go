package server

import (
	"context"
	"log"
	"time"

	"github.com/easylab-platform/easyvcs/store"
)

// mirrorLeaderKey is the advisory-lock key a replica holds to become the
// push-mirror scheduler leader. It is the most-negative int64, far from any repo
// id (which are positive), so leader election never collides with a repo lock.
const mirrorLeaderKey = int64(-1 << 63)

// MirrorSchedulerMode selects how the push-mirror scheduler runs across
// replicas:
//
//	leader  (default on Postgres) only the leader replica runs it, so mirrors
//	        are pushed exactly once; a standby promotes when the leader dies.
//	always  every replica runs it (the pre-HA behaviour). Correct only for a
//	        SINGLE instance, or on SQLite.
//	off     no replica runs it.
type MirrorSchedulerMode string

const (
	MirrorSchedulerLeader MirrorSchedulerMode = "leader"
	MirrorSchedulerAlways MirrorSchedulerMode = "always"
	MirrorSchedulerOff    MirrorSchedulerMode = "off"
)

// ParseMirrorSchedulerMode validates the EASYVCS_MIRROR_SCHEDULER value. An
// empty string yields the backend default: leader on Postgres (a single
// scheduler), always elsewhere (single instance).
func ParseMirrorSchedulerMode(v, driverKind string) (MirrorSchedulerMode, bool) {
	switch v {
	case "":
		if driverKind == store.KindPostgres {
			return MirrorSchedulerLeader, true
		}
		return MirrorSchedulerAlways, true
	case string(MirrorSchedulerLeader):
		return MirrorSchedulerLeader, true
	case string(MirrorSchedulerAlways):
		return MirrorSchedulerAlways, true
	case string(MirrorSchedulerOff):
		return MirrorSchedulerOff, true
	default:
		return "", false
	}
}

// RunMirrorScheduler starts the push-mirror scheduler according to mode. It is
// the entry point cmd/server calls; it blocks until ctx is cancelled.
func RunMirrorScheduler(ctx context.Context, cs *store.CentralStore, mode MirrorSchedulerMode, interval, timeout time.Duration) {
	if interval <= 0 || mode == MirrorSchedulerOff {
		return
	}
	if mode == MirrorSchedulerAlways || cs.DriverKind() != store.KindPostgres {
		RunPushMirrorScheduler(ctx, cs, interval, timeout)
		return
	}
	runLeaderElectedMirrorScheduler(ctx, cs, interval, timeout)
}

// leaderPollInterval is how often a standby retries to become the mirror
// scheduler leader after the leader's lock frees.
const leaderPollInterval = 15 * time.Second

// runLeaderElectedMirrorScheduler elects exactly one scheduler across replicas
// via a Postgres session advisory lock. The leader runs the periodic scheduler
// while it holds the lock; when it loses the lock (or exits), a standby wins
// the next poll and resumes scheduling.
//
// The lock is a SESSION lock on a dedicated connection: Postgres releases it
// automatically when that connection drops (leader crash), so no heartbeat is
// needed. A clean shutdown unlocks explicitly before closing the connection.
func runLeaderElectedMirrorScheduler(ctx context.Context, cs *store.CentralStore, interval, timeout time.Duration) {
	db := cs.SQLDB()
	for ctx.Err() == nil {
		conn, err := db.Conn(ctx)
		if err != nil {
			log.Printf("push-mirror scheduler: standby: connect: %v", err)
			if !sleepCtx(ctx, leaderPollInterval) {
				return
			}
			continue
		}
		var leader bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", mirrorLeaderKey).Scan(&leader); err != nil {
			log.Printf("push-mirror scheduler: standby: try-lock: %v", err)
			_ = conn.Close()
			if !sleepCtx(ctx, leaderPollInterval) {
				return
			}
			continue
		}
		if !leader {
			_ = conn.Close()
			if !sleepCtx(ctx, leaderPollInterval) {
				return
			}
			continue
		}
		log.Printf("push-mirror scheduler: became leader; scheduling every %s", interval)
		RunPushMirrorScheduler(ctx, cs, interval, timeout)
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", mirrorLeaderKey)
		_ = conn.Close()
		return
	}
}

// sleepCtx sleeps for d, returning false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
