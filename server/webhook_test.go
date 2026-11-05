package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// captureSink records delivered events.
type captureSink struct {
	mu   sync.Mutex
	evs  []WebhookEvent
	done chan struct{}
}

func newCaptureSink() *captureSink {
	return &captureSink{done: make(chan struct{}, 16)}
}

func (c *captureSink) Deliver(ev WebhookEvent) {
	c.mu.Lock()
	c.evs = append(c.evs, ev)
	c.mu.Unlock()
	select {
	case c.done <- struct{}{}:
	default:
	}
}

func (c *captureSink) wait(t *testing.T, n int) []WebhookEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.evs)
		c.mu.Unlock()
		if got >= n {
			c.mu.Lock()
			defer c.mu.Unlock()
			return append([]WebhookEvent(nil), c.evs...)
		}
		select {
		case <-c.done:
		case <-deadline:
			c.mu.Lock()
			defer c.mu.Unlock()
			t.Fatalf("only %d events arrived: %+v", len(c.evs), c.evs)
			return nil
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestSignAndVerifyWebhook round-trips the signature.
func TestSignAndVerifyWebhook(t *testing.T) {
	secret := "top-secret"
	body := []byte(`{"event":"rr.opened"}`)
	ts := time.Now().Unix()

	sig := SignWebhook(secret, ts, body)
	if !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("signature = %q", sig)
	}
	if err := VerifyWebhook(secret, ts, body, sig, 5*time.Minute); err != nil {
		t.Fatalf("a fresh signature must verify: %v", err)
	}

	// A different body, secret, or timestamp must not verify.
	if err := VerifyWebhook(secret, ts, []byte(`{"event":"r"}`), sig, 5*time.Minute); err == nil {
		t.Error("a tampered body must not verify")
	}
	if err := VerifyWebhook("other", ts, body, sig, 5*time.Minute); err == nil {
		t.Error("a wrong secret must not verify")
	}
	if err := VerifyWebhook(secret, ts+1, body, sig, 5*time.Minute); err == nil {
		t.Error("a different timestamp must not verify")
	}
	// An OLD timestamp is a replay.
	if err := VerifyWebhook(secret, ts-3600, body, SignWebhook(secret, ts-3600, body), 5*time.Minute); err == nil {
		t.Error("a stale timestamp must be rejected (replay)")
	}
}

// TestWebhookDeliversSignedRequest sends a real HTTP request and verifies what a
// receiver sees.
func TestWebhookDeliversSignedRequest(t *testing.T) {
	type received struct {
		body      []byte
		signature string
		timestamp string
		event     string
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{
			body: b, signature: r.Header.Get("X-EasyVCS-Signature"),
			timestamp: r.Header.Get("X-EasyVCS-Timestamp"),
			event:     r.Header.Get("X-EasyVCS-Event"),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	sink := NewWebhookSink(WebhookConfig{URL: srv.URL, Secret: "s3cret"})
	sink.Deliver(WebhookEvent{Event: "rr.opened", Namespace: "team-a", RR: 7, Head: "abc"})

	select {
	case r := <-got:
		if r.event != "rr.opened" {
			t.Errorf("event header = %q", r.event)
		}
		// The receiver can verify it: the signed material is `<ts>.<body>`.
		var ts int64
		_, _ = fmt.Sscanf(r.timestamp, "%d", &ts)
		if err := VerifyWebhook("s3cret", ts, r.body, r.signature, 5*time.Minute); err != nil {
			t.Errorf("the delivered signature does not verify: %v", err)
		}
		var ev WebhookEvent
		if err := json.Unmarshal(r.body, &ev); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if ev.RR != 7 || ev.Head != "abc" {
			t.Errorf("payload = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no webhook arrived")
	}
}

// TestWebhookFailureDoesNotBlock: a broken receiver must not stall the caller.
func TestWebhookFailureDoesNotBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	sink := NewWebhookSink(WebhookConfig{URL: srv.URL, Secret: "x"})
	start := time.Now()
	sink.Deliver(WebhookEvent{Event: "rr.opened"})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Deliver blocked for %s (it must be asynchronous)", d)
	}
}

// TestWebhookUnreachableIsSilent: an unreachable receiver is logged, never
// propagated (there is no error to propagate from an async delivery).
func TestWebhookUnreachableIsSilent(t *testing.T) {
	sink := NewWebhookSink(WebhookConfig{URL: "http://127.0.0.1:1/nope", Secret: "x"})
	sink.Deliver(WebhookEvent{Event: "rr.opened"}) // must not panic or block
}

// TestNilSinkWhenNoURL: the caller can install the result unconditionally.
func TestNilSinkWhenNoURL(t *testing.T) {
	if s := NewWebhookSink(WebhookConfig{}); s != nil {
		t.Fatal("an empty URL must yield a nil sink")
	}
}

// TestServerEmitsOnRRTransitions wires the sink into the server and drives the
// real HTTP handlers.
func TestServerEmitsOnRRTransitions(t *testing.T) {
	base, mux, token, repo := hostingTestServer(t)
	sink := newCaptureSink()
	base.WithWebhook(sink)

	ws := revision.NewWorkspace(repo)
	snap, rev, err := ws.Commit(revision.CommitParams{
		TreeID: mustEmptyTree(t, ws), Description: "change",
		Author: store.Author{Name: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = snap

	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr", token, map[string]any{
		"source_snapshot": rev.Hash.String(), "target_branch": "main", "title": "t",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create rr = %d %s", rec.Code, rec.Body.String())
	}
	evs := sink.wait(t, 1)
	if evs[0].Event != "rr.opened" {
		t.Errorf("event = %q, want rr.opened", evs[0].Event)
	}
	if evs[0].Namespace != "team-app" {
		t.Errorf("namespace = %q", evs[0].Namespace)
	}
	if evs[0].Head != rev.Hash.String() {
		t.Errorf("head = %q, want the source snapshot %s", evs[0].Head, rev.Hash)
	}
}

// TestServerEmitsTagCreated.
func TestServerEmitsTagCreated(t *testing.T) {
	base, mux, token, repo := hostingTestServer(t)
	sink := newCaptureSink()
	base.WithWebhook(sink)

	ws := revision.NewWorkspace(repo)
	revs, err := repo.ListRevisions()
	if err != nil || len(revs) == 0 {
		t.Fatalf("no revision to tag: %v", err)
	}
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/tags", token, map[string]any{
		"name": "v1.2.3", "target": revs[0].ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create tag = %d %s", rec.Code, rec.Body.String())
	}
	evs := sink.wait(t, 1)
	if evs[0].Event != "tag.created" || evs[0].Tag != "v1.2.3" {
		t.Errorf("event = %+v", evs[0])
	}
	_ = ws
}

// TestWebhookIsOffByDefault: an unconfigured server emits nothing (and must not
// panic when the handlers fire).
func TestWebhookIsOffByDefault(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	ws := revision.NewWorkspace(repo)
	revs, _ := repo.ListRevisions()
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/tags", token, map[string]any{
		"name": "v9", "target": revs[0].ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("tag = %d %s", rec.Code, rec.Body.String())
	}
	_ = ws
}

// mustEmptyTree writes an empty tree.
func mustEmptyTree(t *testing.T, ws *revision.Workspace) object.ID {
	t.Helper()
	id, err := ws.WriteTree(object.NewTree())
	if err != nil {
		t.Fatal(err)
	}
	return id
}
