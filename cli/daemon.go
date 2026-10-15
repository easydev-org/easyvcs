package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/easylab-platform/easyvcs/server"
	"github.com/easylab-platform/easyvcs/store"
)

// DaemonSocketPath is the unix socket an `easyvcs daemon` listens on. It lives
// under the store home so a client can find it from EASYVCS_HOME alone.
func DaemonSocketPath() string {
	return filepath.Join(store.HomeDir(), "easyvcs.sock")
}

// cmdDaemon runs the easyvcs engine as a long-lived local process, serving the
// SAME HTTP API as the remote server but over a unix socket and against the
// LOCAL SQLite store. The store is opened ONCE, so repeated CLI calls avoid the
// per-command cold start (open + WAL + migrate) and share one process's cache.
//
// It is optional: every command still works without a daemon (direct SQLite).
// A client targets the daemon by registering a remote URL of the form
// `unix://<socket>` (or by pointing EASYVCS_DAEMON at the socket).
func cmdDaemon(c *ctx) {
	// The daemon owns a single long-lived store, opened WITH WAL (it writes).
	cs, err := store.OpenDefault()
	if err != nil {
		c.fatal("daemon: open store:", err)
	}
	if err := cs.SetWAL(); err != nil {
		c.fatal("daemon: enable WAL:", err)
	}
	c.cs = cs

	sock := DaemonSocketPath()
	// A stale socket from a crashed daemon blocks Listen; remove it. (A live
	// daemon's socket would be dialable — but the user asked to start one, so we
	// surface a clear error if the path is a live socket.)
	if fi, err := os.Stat(sock); err == nil {
		if fi.Mode()&os.ModeSocket != 0 {
			if conn, derr := net.DialTimeout("unix", sock, 200*time.Millisecond); derr == nil {
				_ = conn.Close()
				c.fatalf("daemon: %s is already in use (another daemon is running)", sock)
			}
		}
		_ = os.Remove(sock)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		c.fatal("daemon: mkdir:", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		c.fatal("daemon: listen:", err)
	}
	defer func() { _ = os.Remove(sock) }()

	srv := server.New(cs, nil)
	httpSrv := &http.Server{Handler: srv.Handler()}

	fmt.Printf("easyvcs daemon listening on %s (store %s)\n", sock, store.HomeDir())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		fmt.Println("easyvcs daemon stopped")
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			c.fatal("daemon:", err)
		}
	}
}

// daemonClient returns an HTTP client that talks to the local daemon over its
// unix socket, plus the base URL to use. The base is "http://unix" (the host is
// irrelevant; the dialer routes every request to the socket).
func daemonClient() (*http.Client, string, bool) {
	sock := os.Getenv("EASYVCS_DAEMON")
	if sock == "" {
		sock = DaemonSocketPath()
	}
	if _, err := os.Stat(sock); err != nil {
		return nil, "", false // no daemon
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &http.Client{Transport: tr}, "http://unix", true
}

// isDaemonURL reports whether a remote URL names the local daemon.
func isDaemonURL(u string) bool { return strings.HasPrefix(u, "unix://") }
