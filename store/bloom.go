package store

import (
	"encoding/base64"
	"hash/fnv"
	"math"

	"github.com/easylab-platform/easyvcs/object"
)

// ObjectBloom is a compact probabilistic set of object ids used by the sync
// protocol: advertise returns the server's object set as a Bloom filter, and a
// pushing client uses it to SKIP objects the server already holds (object-level
// delta) without sending the full id list. False positives only cause a client
// to skip an object it actually needs — the server detects the resulting
// missing-object error on apply and the client can retry with a full push, so
// the filter is sized generously (1% false-positive at the expected count).
type ObjectBloom struct {
	bits []uint64
	k    int // number of hash probes
	m    int // number of bits
}

// NewObjectBloom sizes a filter for n expected elements at ~1% false positives.
func NewObjectBloom(n int) *ObjectBloom {
	if n < 1 {
		n = 1
	}
	// m = -n ln p / (ln 2)^2 ; k = (m/n) ln 2, with p = 0.01.
	m := int(math.Ceil(-float64(n) * math.Log(0.01) / (math.Ln2 * math.Ln2)))
	k := int(math.Round(float64(m) / float64(n) * math.Ln2))
	if k < 1 {
		k = 1
	}
	return &ObjectBloom{bits: make([]uint64, (m+63)/64), k: k, m: m}
}

// Add inserts an id.
func (b *ObjectBloom) Add(id object.ID) {
	h1, h2 := bloomHashes(id)
	for i := 0; i < b.k; i++ {
		pos := uint64(h1+uint64(i)*h2) % uint64(b.m)
		b.bits[pos/64] |= 1 << (pos % 64)
	}
}

// MayContain reports whether the id is possibly in the set (false positives
// possible; false negatives never).
func (b *ObjectBloom) MayContain(id object.ID) bool {
	if b == nil || b.m == 0 {
		return false
	}
	h1, h2 := bloomHashes(id)
	for i := 0; i < b.k; i++ {
		pos := uint64(h1+uint64(i)*h2) % uint64(b.m)
		if b.bits[pos/64]&(1<<(pos%64)) == 0 {
			return false
		}
	}
	return true
}

// Encode serializes the filter (m, k, bits) as a base64 string for transport.
func (b *ObjectBloom) Encode() string {
	if b == nil {
		return ""
	}
	// Layout: [4-byte m big-endian][1-byte k][bits...] then base64.
	buf := make([]byte, 5+len(b.bits)*8)
	buf[0] = byte(b.m >> 24)
	buf[1] = byte(b.m >> 16)
	buf[2] = byte(b.m >> 8)
	buf[3] = byte(b.m)
	buf[4] = byte(b.k)
	for i, w := range b.bits {
		off := 5 + i*8
		for j := 0; j < 8; j++ {
			buf[off+j] = byte(w >> (8 * j))
		}
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// DecodeObjectBloom reverses Encode. An empty string yields nil (no filter).
func DecodeObjectBloom(s string) *ObjectBloom {
	if s == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) < 5 {
		return nil
	}
	m := int(raw[0])<<24 | int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	k := int(raw[4])
	words := (m + 63) / 64
	if m <= 0 || k <= 0 || len(raw) < 5+words*8 {
		return nil
	}
	bits := make([]uint64, words)
	for i := range bits {
		off := 5 + i*8
		var w uint64
		for j := 0; j < 8; j++ {
			w |= uint64(raw[off+j]) << (8 * j)
		}
		bits[i] = w
	}
	return &ObjectBloom{bits: bits, k: k, m: m}
}

// bloomHashes returns two independent 64-bit hashes of an id (double hashing).
func bloomHashes(id object.ID) (uint64, uint64) {
	s := id.String()
	h1 := fnv.New64a()
	_, _ = h1.Write([]byte(s))
	h2 := fnv.New64()
	_, _ = h2.Write([]byte(s))
	a := h1.Sum64()
	b := h2.Sum64()
	if b == 0 {
		b = 0x9e3779b97f4a7c15
	}
	return a, b
}

// ObjectBloom returns a Bloom filter of every object id in the store. The
// objects table is global, so this is the set the server can offer any client.
func (s *CentralStore) ObjectBloom() (*ObjectBloom, error) {
	var rows []string
	if err := s.d.gdb.Model(&objectRow{}).Pluck("sha", &rows).Error; err != nil {
		return nil, err
	}
	bl := NewObjectBloom(len(rows))
	for _, r := range rows {
		if id, err := object.HexToID(r); err == nil {
			bl.Add(id)
		}
	}
	return bl, nil
}

// AllObjectIDs returns every object id in the store (used by tests and callers
// that want the exact set rather than a Bloom approximation).
func (s *CentralStore) AllObjectIDs() ([]object.ID, error) {
	var rows []string
	if err := s.d.gdb.Model(&objectRow{}).Pluck("sha", &rows).Error; err != nil {
		return nil, err
	}
	out := make([]object.ID, 0, len(rows))
	for _, r := range rows {
		if id, err := object.HexToID(r); err == nil {
			out = append(out, id)
		}
	}
	return out, nil
}
