// Package syncproto holds the wire types shared by the easyvcs server and the
// CLI sync client (advertise / fetch / push). Both sides import THESE types so
// the contract is defined once; a change to a field is a change in one place.
//
// The bodies of fetch/push are `transfer.Bundle` (also shared). This package
// covers the JSON advertise request/response and the file-op commit/snapshot
// bodies used by the hosting REST surface.
package syncproto

import "github.com/easylab-platform/easyvcs/store"

// AdvertiseReq is the request body of POST …/advertise and …/fetch.
type AdvertiseReq struct {
	// Have lists the revision ids the client already holds (fetch: skip these).
	Have []string `json:"have"`
	// HaveObjects lists object ids the client already holds (fetch delta).
	HaveObjects []string `json:"have_objects"`
	// WantChanges, when set, restricts a fetch to those revisions (and their
	// ancestors).
	WantChanges []string `json:"want_changes,omitempty"`
}

// AdvertiseResp is the response body of POST …/advertise.
type AdvertiseResp struct {
	Repo    string       `json:"repo"`
	Changes []string     `json:"changes"`
	Refs    []*store.Ref `json:"refs"`
	// ChangeHashes maps each advertised revision id to its CURRENT snapshot
	// hash (hex). A push client uses it to decide which revisions the server
	// already holds *at the same content*: because rewriting (rebase / amend /
	// squash) keeps the revision id stable and changes only the snapshot, an
	// id-only comparison would wrongly skip a rewritten revision and silently
	// drop its new snapshot. Omitted (nil) by older servers, in which case a
	// client falls back to id-only skipping.
	ChangeHashes map[string]string `json:"change_hashes,omitempty"`
	// ObjectBloom is a base64 Bloom filter of every object id the server holds.
	// A pushing client uses it to SKIP objects the server already has (object-
	// level delta) without a full id list. Absent (empty) => the client sends
	// everything (older servers, or a store with no objects yet).
	ObjectBloom string `json:"object_bloom,omitempty"`
}

// FileOp is one file operation in a commit/snapshot request body.
type FileOp struct {
	// Path is the repo-relative path (forward slashes).
	Path string `json:"path"`
	// Operation is "create" | "update" | "delete" (empty = update).
	Operation string `json:"operation"`
	// Content is the text body (used when ContentBytes is empty).
	Content string `json:"content"`
	// ContentBytes is base64; it wins over Content and is binary-safe.
	ContentBytes string `json:"content_bytes"`
}

// CommitReq is the body of POST …/commit (working-copy-free write path). The
// scope is the ?path= query, NOT a body field.
type CommitReq struct {
	Branch     string        `json:"branch"`
	Message    string        `json:"message"`
	RevisionID string        `json:"revision_id"`
	Author     *store.Author `json:"author"`
	Parents    []string      `json:"parents"`
	Files      []FileOp      `json:"files"`
}

// SnapshotReq is the body of POST …/snapshot (propose without a branch). The
// scope is the ?path= query.
type SnapshotReq struct {
	Message string        `json:"message"`
	Author  *store.Author `json:"author"`
	Parents []string      `json:"parents"`
	Files   []FileOp      `json:"files"`
}
