package object

import (
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// Conflict is an N-way merge conflict, expressed as a set of negative terms
// (the base versions, "removes") and positive terms (the side versions,
// "adds"). This mirrors jj's `Merge<T>` where a 3-way conflict is represented
// as [-base, +ours, +theirs]; a conflict can generalize to any number of
// terms. Each term references a blob id plus a human label describing where it
// came from.
//
// A term may also carry a Path (see Term.Path). Path is empty for a plain
// content conflict at the conflicted path. It is non-empty for a conflict whose
// terms live at DIFFERENT paths — notably a rename conflict, where the same
// base blob reappears at different new paths on each side. The path is part of
// the conflict's canonical encoding, so two conflicts that differ only by term
// path are distinct objects and hash differently. A conflict whose terms all
// have an empty Path encodes (and hashes) byte-for-byte as before, so existing
// object ids are stable.
type Conflict struct {
	Removes []Term
	Adds    []Term
}

// Term is one side of a conflict: a blob id plus a label. Path, when non-empty,
// names the repo-relative path this term lives at (used by rename conflicts);
// an empty Path means "the conflict's own path" (the legacy, path-less form).
type Term struct {
	ID    ID
	Label string
	Path  string
}

// NewConflictFrom3Way builds a conflict from a classic 3-way merge:
// base is negative; ours and theirs are positive.
func NewConflictFrom3Way(base, ours, theirs ID) *Conflict {
	return &Conflict{
		Removes: []Term{{ID: base, Label: "base"}},
		Adds: []Term{
			{ID: ours, Label: "ours"},
			{ID: theirs, Label: "theirs"},
		},
	}
}

// Numeric returns the number of terms (removes + adds).
func (c *Conflict) Numeric() int { return len(c.Removes) + len(c.Adds) }

// HasPaths reports whether any term carries a non-empty Path. It selects the
// encoding/hash form: path-less conflicts keep the legacy byte layout.
func (c *Conflict) HasPaths() bool {
	for _, t := range c.Removes {
		if t.Path != "" {
			return true
		}
	}
	for _, t := range c.Adds {
		if t.Path != "" {
			return true
		}
	}
	return false
}

// pickTerm selects the add term at the given index (0-based among Adds).
func (c *Conflict) pickTerm(idx int) (Term, bool) {
	if idx < 0 || idx >= len(c.Adds) {
		return Term{}, false
	}
	return c.Adds[idx], true
}

// resolveToAdd returns a conflict reduced to a single side by index.
func resolveToAdd(c *Conflict, idx int) (ID, bool) {
	t, ok := c.pickTerm(idx)
	return t.ID, ok
}

// conflictMagic is the canonical serialization header for the legacy, path-less
// conflict form (v1). conflictMagicV2 is the path-bearing form. A conflict is
// encoded as v1 whenever no term has a Path, so its bytes (and therefore its
// content id) are unchanged from earlier releases.
const (
	conflictMagic   = "easyvcs-conflict-v1"
	conflictMagicV2 = "easyvcs-conflict-v2"
)

// writeConflictBody writes the legacy (path-less) body: counts plus id/label per
// term. It is shared by the v1 encoding and by the hash of a path-less conflict.
func writeConflictBody(w io.Writer, c *Conflict) {
	fmt.Fprintf(w, "%d\n", len(c.Removes))
	for _, t := range c.Removes {
		fmt.Fprintf(w, "%s\n%s\n", hex.EncodeToString(t.ID[:]), t.Label)
	}
	fmt.Fprintf(w, "%d\n", len(c.Adds))
	for _, t := range c.Adds {
		fmt.Fprintf(w, "%s\n%s\n", hex.EncodeToString(t.ID[:]), t.Label)
	}
}

// writeConflictBodyV2 writes the path-bearing body: counts plus id/label/path
// per term.
func writeConflictBodyV2(w io.Writer, c *Conflict) {
	fmt.Fprintf(w, "%d\n", len(c.Removes))
	for _, t := range c.Removes {
		fmt.Fprintf(w, "%s\n%s\n%s\n", hex.EncodeToString(t.ID[:]), t.Label, t.Path)
	}
	fmt.Fprintf(w, "%d\n", len(c.Adds))
	for _, t := range c.Adds {
		fmt.Fprintf(w, "%s\n%s\n%s\n", hex.EncodeToString(t.ID[:]), t.Label, t.Path)
	}
}

// writeConflictForHash writes the canonical body a conflict hashes over. It
// emits the legacy body when no term has a path (so path-less ids are stable)
// and the path-bearing body otherwise.
func writeConflictForHash(w io.Writer, c *Conflict) {
	if c.HasPaths() {
		writeConflictBodyV2(w, c)
		return
	}
	writeConflictBody(w, c)
}

// encodeConflictBytes produces the canonical byte form of a conflict.
func encodeConflictBytes(c *Conflict) []byte {
	var b strings.Builder
	if c.HasPaths() {
		b.WriteString(conflictMagicV2)
		b.WriteByte('\n')
		writeConflictBodyV2(&b, c)
		return []byte(b.String())
	}
	b.WriteString(conflictMagic)
	b.WriteByte('\n')
	writeConflictBody(&b, c)
	return []byte(b.String())
}

// decodeConflictBytes parses the canonical byte form back into a Conflict. It
// dispatches on the magic header so both the legacy (v1) and path-bearing (v2)
// forms round-trip.
func decodeConflictBytes(payload []byte) *Conflict {
	p := string(payload)
	switch {
	case strings.HasPrefix(p, conflictMagicV2):
		return decodeConflictV2(strings.TrimPrefix(p, conflictMagicV2))
	case strings.HasPrefix(p, conflictMagic):
		return decodeConflictV1(strings.TrimPrefix(p, conflictMagic))
	default:
		return &Conflict{}
	}
}

// decodeConflictV1 parses a legacy body (no path per term).
func decodeConflictV1(body string) *Conflict {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	// Drop the leading empty line produced by the magic's trailing newline.
	if len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	i := 0
	readTerms := func() []Term {
		n := 0
		if i < len(lines) {
			// A malformed count line parses as 0 terms; the surrounding
			// line-count guard already bounds the loop.
			_, _ = fmt.Sscanf(lines[i], "%d", &n)
			i++
		}
		terms := make([]Term, 0, n)
		for k := 0; k < n && i+1 < len(lines); k++ {
			var id ID
			if b, err := hex.DecodeString(lines[i]); err == nil && len(b) == len(id) {
				copy(id[:], b)
			}
			label := lines[i+1]
			i += 2
			terms = append(terms, Term{ID: id, Label: label})
		}
		return terms
	}
	return &Conflict{Removes: readTerms(), Adds: readTerms()}
}

// decodeConflictV2 parses a path-bearing body (id/label/path per term).
func decodeConflictV2(body string) *Conflict {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	i := 0
	readTerms := func() []Term {
		n := 0
		if i < len(lines) {
			_, _ = fmt.Sscanf(lines[i], "%d", &n)
			i++
		}
		terms := make([]Term, 0, n)
		for k := 0; k < n && i+2 < len(lines); k++ {
			var id ID
			if b, err := hex.DecodeString(lines[i]); err == nil && len(b) == len(id) {
				copy(id[:], b)
			}
			label := lines[i+1]
			path := lines[i+2]
			i += 3
			terms = append(terms, Term{ID: id, Label: label, Path: path})
		}
		return terms
	}
	return &Conflict{Removes: readTerms(), Adds: readTerms()}
}

// IsConflictPayload reports whether a payload looks like an encoded conflict
// (either the legacy or the path-bearing form).
func IsConflictPayload(payload []byte) bool {
	p := string(payload)
	return strings.HasPrefix(p, conflictMagicV2) || strings.HasPrefix(p, conflictMagic)
}

// IsConflictObject reports whether an object kind is a conflict.
func IsConflictObject(k Kind) bool { return k == KindConflict }
