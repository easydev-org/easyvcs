package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Webhooks.
//
// A VCS is only useful as a CI trigger if it TELLS someone when a change
// happens. Rather than make the platform poll (or rely on a client to POST an
// event by hand), the server emits an HMAC-signed webhook on the transitions a
// pipeline cares about:
//
//	rr.opened    a Revision Request was created
//	rr.synced    its source snapshot was updated (a new proposal revision)
//	rr.merged    it landed on the target branch
//	tag.created  a tag was created
//
// The signature is HMAC-SHA256 over `<timestamp>.<body>`, sent as
// `X-EasyVCS-Signature: sha256=<hex>`; the timestamp is `X-EasyVCS-Timestamp`.
// A receiver must reject an old timestamp (replay) and compare in constant time.
//
// Delivery is BEST EFFORT and asynchronous: a slow or broken receiver must never
// block or fail a VCS write. Every attempt is logged.

// WebhookEvent is the payload sent to a registered endpoint.
type WebhookEvent struct {
	// Event is "rr.opened" | "rr.synced" | "rr.merged" | "tag.created".
	Event string `json:"event"`
	// Namespace, Ref, Tag, RR identify what happened.
	Namespace string `json:"namespace"`
	Ref       string `json:"ref,omitempty"`
	Tag       string `json:"tag,omitempty"`
	RR        int64  `json:"rr,omitempty"`
	// Base/Head are the pinned snapshots (base = the target tip at creation).
	Base string `json:"base,omitempty"`
	Head string `json:"head,omitempty"`
	// ChangedPaths is the set of paths the change touched (for path filters).
	ChangedPaths []string `json:"changed_paths,omitempty"`
	// Actor is the user who caused the event, when known.
	Actor string `json:"actor,omitempty"`
	// DeliveredAt is unix millis.
	DeliveredAt int64 `json:"delivered_at"`
}

// WebhookSink delivers events to a URL.
//
// It is an interface so a deployment can swap the transport (e.g. an in-process
// queue) and tests can capture events without HTTP.
type WebhookSink interface {
	Deliver(ev WebhookEvent)
}

// WebhookConfig configures the built-in HTTP sink.
type WebhookConfig struct {
	// URL is the endpoint the events are POSTed to. Empty disables webhooks.
	URL string
	// Secret signs the payload. Empty means unsigned delivery (only acceptable
	// for a loopback endpoint).
	Secret string
	// Timeout bounds one delivery (default 5s).
	Timeout time.Duration
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

// NewWebhookSink builds the HTTP sink. A nil sink is returned when no URL is
// configured, so the caller can store it unconditionally.
func NewWebhookSink(cfg WebhookConfig) WebhookSink {
	if cfg.URL == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &httpWebhookSink{url: cfg.URL, secret: cfg.Secret, client: client}
}

type httpWebhookSink struct {
	url    string
	secret string
	client *http.Client
}

// Deliver sends the event ASYNCHRONOUSLY.
//
// A VCS write must not wait for (or fail because of) a receiver: the request is
// detached from the caller's context and its outcome only reaches the log.
func (s *httpWebhookSink) Deliver(ev WebhookEvent) {
	if ev.DeliveredAt == 0 {
		ev.DeliveredAt = time.Now().UTC().UnixMilli()
	}
	body, err := json.Marshal(ev)
	if err != nil {
		log.Printf("easyvcs-webhook: encode %s: %v", ev.Event, err)
		return
	}
	go s.send(ev.Event, body)
}

// send performs one delivery attempt.
func (s *httpWebhookSink) send(event string, body []byte) {
	// A bounded, detached context: the write that triggered this is already done,
	// so there is nothing to cancel it against.
	ctx, cancel := context.WithTimeout(context.Background(), s.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		log.Printf("easyvcs-webhook %s: build request: %v", event, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "easyvcs-webhook/1")
	req.Header.Set("X-EasyVCS-Event", event)
	ts := time.Now().Unix()
	req.Header.Set("X-EasyVCS-Timestamp", fmt.Sprintf("%d", ts))
	if s.secret != "" {
		req.Header.Set("X-EasyVCS-Signature", SignWebhook(s.secret, ts, body))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("easyvcs-webhook %s: %v", event, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Printf("easyvcs-webhook %s: receiver returned %d", event, resp.StatusCode)
	}
}

// SignWebhook computes the signature header value for a payload.
//
// The timestamp is part of the signed material, so a captured request cannot be
// replayed later without the receiver noticing.
func SignWebhook(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhook checks a delivered request's signature and freshness.
//
// maxAge bounds the clock skew / replay window (e.g. 5 minutes).
func VerifyWebhook(secret string, timestamp int64, body []byte, signature string, maxAge time.Duration) error {
	if secret == "" {
		return nil // unsigned delivery: nothing to verify
	}
	age := time.Since(time.Unix(timestamp, 0))
	if maxAge > 0 && (age > maxAge || age < -maxAge) {
		return fmt.Errorf("webhook: timestamp is outside the allowed window (%s old)", age.Round(time.Second))
	}
	want := SignWebhook(secret, timestamp, body)
	if !hmac.Equal([]byte(signature), []byte(want)) {
		return fmt.Errorf("webhook: signature mismatch")
	}
	return nil
}

// WithWebhook installs a webhook sink.
func (s *Server) WithWebhook(sink WebhookSink) *Server {
	s.webhook = sink
	return s
}

// emitWebhook delivers an event when a sink is installed.
func (s *Server) emitWebhook(ev WebhookEvent) {
	if s.webhook == nil {
		return
	}
	if ev.DeliveredAt == 0 {
		ev.DeliveredAt = time.Now().UTC().UnixMilli()
	}
	s.webhook.Deliver(ev)
}
