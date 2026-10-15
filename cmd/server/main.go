// Command server is the thin executable entrypoint for the EasyVCS VCS
// protocol server. All protocol logic lives in github.com/easylab-platform/
// easyvcs/server; this file only parses flags, opens the central store, and
// starts the HTTP listener (default :8996, h1 + h2c), and shuts it down
// gracefully on SIGINT/SIGTERM.
//
// Provide the store via EASYVCS_DB_DRIVER (sqlite|postgres|mysql) and
// EASYVCS_DB_DSN. Auth isn't configured by env here: tokens/users are resolved
// from the store (store.LookupToken) on each request. Repository access uses a
// single role model (owner / maintainer / developer, see store.RoleOf): the
// namespace owner is `owner`, repo_members grant maintainer/developer, and a
// public repo gives everyone developer-level read/propose. When the store has no
// users the instance is open (anonymous read/write). The full endpoint contract
// is in docs/api.md; user administration additionally needs EASYVCS_ADMIN_TOKEN.
//
// Operational env: EASYVCS_MAX_BODY bounds request bodies (bytes; default
// 512MiB). Set EASYVCS_TRUSTED_PROXY to honor X-Forwarded-For for audit logs
// (only when running behind a reverse proxy you control). The push-mirror
// scheduler is controlled by EASYVCS_MIRROR_INTERVAL (seconds; unset = 5m,
// <=0 = disabled) and EASYVCS_MIRROR_TIMEOUT (seconds; unset = 2m).
//
// Probes (unauthenticated): GET /api/v1/healthz (liveness), GET /api/v1/readyz
// (readiness; 503 when the store is unreachable).
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/easylab-platform/easyvcs/server"
	"github.com/easylab-platform/easyvcs/store"
)

// shutdownTimeout bounds how long a graceful shutdown waits for in-flight
// requests (e.g. a large push) before forcing the listener closed.
const shutdownTimeout = 15 * time.Second

func main() {
	addr := flag.String("addr", ":8996", "listen address")
	flag.Parse()

	cs, err := store.OpenDriver(store.DriverConfig{
		Kind: envOr("EASYVCS_DB_DRIVER", store.KindSQLite),
		DSN:  envOr("EASYVCS_DB_DSN", ""),
	})
	if err != nil {
		log.Fatal("open store:", err)
	}
	if err := cs.SetWAL(); err != nil {
		log.Fatal("enable WAL:", err)
	}

	cs.WithAsyncIndex()
	srv := server.New(cs, nil).WithAdvertiseTTL(2 * time.Second)
	if mb := envOr("EASYVCS_MAX_BODY", ""); mb != "" {
		if n, err := strconv.ParseInt(mb, 10, 64); err == nil {
			srv.SetMaxBody(n)
		} else {
			log.Fatalf("EASYVCS_MAX_BODY: invalid size %q", mb)
		}
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute, // large pushes are slow to upload
		WriteTimeout:      10 * time.Minute, // large fetches are slow to stream
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	// Shut down gracefully on SIGINT/SIGTERM: stop accepting new connections,
	// let in-flight requests finish (bounded by shutdownTimeout), then close the
	// store so a file-backed SQLite DB is flushed.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("easyvcs-server listening on %s (db %s)", *addr, store.DBPath())
		errCh <- httpSrv.ListenAndServe()
	}()

	// Optional push-mirror scheduler: periodically force-push every configured
	// push mirror. EASYVCS_MIRROR_INTERVAL is the period in seconds (<=0 or
	// unset disables it); EASYVCS_MIRROR_TIMEOUT bounds a single push.
	// EASYVCS_MIRROR_SCHEDULER selects how it runs across replicas
	// (leader|always|off; default: leader on Postgres, always on SQLite), so a
	// multi-replica Postgres deployment pushes each mirror exactly once.
	mode, ok := server.ParseMirrorSchedulerMode(os.Getenv("EASYVCS_MIRROR_SCHEDULER"), cs.DriverKind())
	if !ok {
		log.Fatalf("EASYVCS_MIRROR_SCHEDULER: invalid value %q (leader|always|off)", os.Getenv("EASYVCS_MIRROR_SCHEDULER"))
	}
	go server.RunMirrorScheduler(ctx, cs, mode, mirrorInterval(), mirrorTimeout())

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	case <-ctx.Done():
		log.Printf("easyvcs-server shutting down (graceful, up to %s)", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}

	if err := cs.Close(); err != nil {
		log.Printf("close store: %v", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// mirrorInterval returns the push-mirror scheduler period. Unset => the default
// (5m); a value <= 0 disables the scheduler.
func mirrorInterval() time.Duration {
	v := os.Getenv("EASYVCS_MIRROR_INTERVAL")
	if v == "" {
		return 5 * time.Minute
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("EASYVCS_MIRROR_INTERVAL: invalid seconds %q", v)
	}
	return time.Duration(n) * time.Second
}

// mirrorTimeout bounds a single scheduled push (unset => 2m).
func mirrorTimeout() time.Duration {
	v := os.Getenv("EASYVCS_MIRROR_TIMEOUT")
	if v == "" {
		return 2 * time.Minute
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("EASYVCS_MIRROR_TIMEOUT: invalid seconds %q", v)
	}
	return time.Duration(n) * time.Second
}
