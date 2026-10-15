package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/easylab-platform/easyvcs/server"
	"github.com/easylab-platform/easyvcs/store"
)

// The daemon serves the engine API over a unix socket; a client that targets
// that socket (via EASYVCS_DAEMON / the daemonClient dialer) reaches the SAME
// store. This exercises the transport end to end without a real process.
func TestDaemonUnixSocketServes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EASYVCS_HOME", home)
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	sock := filepath.Join(home, "easyvcs.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	srv := server.New(cs, nil)
	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() { _ = httpSrv.Serve(ln) }()
	defer func() { _ = httpSrv.Shutdown(context.Background()) }()
	// Wait for the socket to accept.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Point the client at the daemon socket and hit a trivial endpoint.
	t.Setenv("EASYVCS_DAEMON", sock)
	cl, base, ok := daemonClient()
	if !ok {
		t.Fatal("daemonClient did not find the socket")
	}
	if base != "http://unix" {
		t.Fatalf("daemon base = %q", base)
	}
	resp, err := cl.Get(base + "/api/v1/healthz")
	if err != nil {
		t.Fatalf("get healthz over the socket: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
}

// daemonClient reports "no daemon" when the socket is absent.
func TestDaemonClientAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EASYVCS_HOME", home)
	t.Setenv("EASYVCS_DAEMON", filepath.Join(home, "nope.sock"))
	if _, _, ok := daemonClient(); ok {
		t.Fatal("daemonClient must report absent when the socket is missing")
	}
	_ = os.Remove
}

// isDaemonURL recognizes the unix:// scheme.
func TestIsDaemonURL(t *testing.T) {
	if !isDaemonURL("unix:///tmp/x.sock") {
		t.Fatal("unix:// should be a daemon URL")
	}
	if isDaemonURL("http://host") {
		t.Fatal("http:// is not a daemon URL")
	}
}
