package revision

import "strings"

// lineMerge3 performs a diff3-style 3-way line merge of base/ours/theirs.
//
// It returns:
//   - merged: the merged lines when the two sides touch DISJOINT regions (no
//     overlap), so a textual merge is safe;
//   - clean:  true when `merged` is the fully merged result and there are no
//     overlapping (conflicting) regions.
//
// When the two sides change OVERLAPPING regions, clean is false and `merged`
// holds the base lines of the first overlapping region (callers fall back to a
// first-class conflict object rather than emitting text markers).
//
// The algorithm: compute each side's edit script against the base, map the
// changed base ranges to intervals, and merge. Non-overlapping intervals are
// applied in order; overlapping intervals are a conflict.
func lineMerge3(base, ours, theirs []string) (merged []string, clean bool) {
	oEdits := sideEdits(base, ours)
	tEdits := sideEdits(base, theirs)

	// Fast paths.
	if len(oEdits) == 0 {
		return theirs, true // ours == base
	}
	if len(tEdits) == 0 {
		return ours, true // theirs == base
	}

	var out []string
	oi, ti := 0, 0
	pos := 0 // current base line index consumed
	for oi < len(oEdits) || ti < len(tEdits) {
		// The next edit on each side (or +inf when exhausted).
		var oStart, tStart int
		if oi < len(oEdits) {
			oStart = oEdits[oi].start
		} else {
			oStart = 1 << 30
		}
		if ti < len(tEdits) {
			tStart = tEdits[ti].start
		} else {
			tStart = 1 << 30
		}

		if oStart <= tStart && oi < len(oEdits) {
			// Copy unchanged base lines up to this edit's start.
			for pos < oEdits[oi].start {
				out = append(out, base[pos])
				pos++
			}
			oe := oEdits[oi]
			// Does the other side also edit an overlapping base range?
			if ti < len(tEdits) && rangesOverlap(oe.start, oe.end, tEdits[ti].start, tEdits[ti].end) {
				// Overlapping edits => conflict; stop textual merging.
				return out, false
			}
			out = append(out, oe.lines...)
			pos = oe.end
			oi++
		} else if ti < len(tEdits) {
			for pos < tEdits[ti].start {
				out = append(out, base[pos])
				pos++
			}
			te := tEdits[ti]
			if oi < len(oEdits) && rangesOverlap(te.start, te.end, oEdits[oi].start, oEdits[oi].end) {
				return out, false
			}
			out = append(out, te.lines...)
			pos = te.end
			ti++
		}
	}
	// Tail.
	for pos < len(base) {
		out = append(out, base[pos])
		pos++
	}
	return out, true
}

// sideEdit is a changed base range [start,end) replaced by `lines`.
type sideEdit struct {
	start int // base line index (0-based), inclusive
	end   int // base line index, exclusive (== start for a pure insertion)
	lines []string
}

// sideEdits derives the changed base ranges of `side` vs `base` from the LCS
// edit script. A run of opDel/opAdd lines becomes one edit: the deleted base
// range [start,end) is replaced by the added lines.
func sideEdits(base, side []string) []sideEdit {
	ops := lcsOps(base, side)
	var edits []sideEdit
	basePos := 0
	i := 0
	for i < len(ops) {
		switch ops[i].kind {
		case opKeep:
			basePos++
			i++
		default:
			start := basePos
			var lines []string
			for i < len(ops) && ops[i].kind != opKeep {
				if ops[i].kind == opDel {
					basePos++
				} else {
					lines = append(lines, ops[i].newLine)
				}
				i++
			}
			edits = append(edits, sideEdit{start: start, end: basePos, lines: lines})
		}
	}
	return edits
}

// rangesOverlap reports whether the half-open base ranges [a1,a2) and [b1,b2)
// overlap. Two PURE INSERTIONS at the same position (a1==a2==b1==b2) count as
// overlapping (both want to insert at one spot).
func rangesOverlap(a1, a2, b1, b2 int) bool {
	if a1 == a2 && b1 == b2 {
		return a1 == b1 // both insert at the same point
	}
	return a1 < b2 && b1 < a2
}

// mergeFileText attempts a textual 3-way merge of two blobs against a base.
// It returns the merged bytes and true when the merge is clean; false means the
// sides overlap (the caller should build a conflict object).
//
// Binary blobs (or a missing base) are never text-merged.
func mergeFileText(base, ours, theirs []byte) ([]byte, bool) {
	if IsBinary(base) || IsBinary(ours) || IsBinary(theirs) {
		return nil, false
	}
	b := splitLines(base)
	o := splitLines(ours)
	t := splitLines(theirs)
	merged, clean := lineMerge3(b, o, t)
	if !clean {
		return nil, false
	}
	if len(merged) == 0 {
		return []byte{}, true
	}
	return []byte(strings.Join(merged, "\n") + "\n"), true
}

// lineMerge3Marked is like lineMerge3 but NEVER bails on overlap: non-overlapping
// regions are auto-merged and each OVERLAPPING region becomes a marker block
// (git/diff3 style), so the rendered file is mostly merged with only the
// genuinely conflicting hunks marked. It returns the output lines and whether
// any conflict block was emitted.
func lineMerge3Marked(base, ours, theirs []string) ([]string, bool) {
	oEdits := sideEdits(base, ours)
	tEdits := sideEdits(base, theirs)
	if len(oEdits) == 0 {
		return theirs, false
	}
	if len(tEdits) == 0 {
		return ours, false
	}

	var out []string
	conflicted := false
	oi, ti, pos := 0, 0, 0
	copyBase := func(to int) {
		for pos < to && pos < len(base) {
			out = append(out, base[pos])
			pos++
		}
	}
	for oi < len(oEdits) || ti < len(tEdits) {
		oStart := int(1) << 30
		if oi < len(oEdits) {
			oStart = oEdits[oi].start
		}
		tStart := 1 << 30
		if ti < len(tEdits) {
			tStart = tEdits[ti].start
		}
		if oStart < tStart {
			copyBase(oStart)
			oe := oEdits[oi]
			if ti < len(tEdits) && rangesOverlap(oe.start, oe.end, tEdits[ti].start, tEdits[ti].end) {
				block, nOI, nTI, end := conflictBlock(base, oEdits, tEdits, oi, ti)
				out = append(out, block...)
				oi, ti, pos = nOI, nTI, end
				conflicted = true
				continue
			}
			out = append(out, oe.lines...)
			pos = oe.end
			oi++
		} else if ti < len(tEdits) {
			copyBase(tStart)
			te := tEdits[ti]
			if oi < len(oEdits) && rangesOverlap(te.start, te.end, oEdits[oi].start, oEdits[oi].end) {
				block, nOI, nTI, end := conflictBlock(base, oEdits, tEdits, oi, ti)
				out = append(out, block...)
				oi, ti, pos = nOI, nTI, end
				conflicted = true
				continue
			}
			out = append(out, te.lines...)
			pos = te.end
			ti++
		}
	}
	copyBase(len(base))
	return out, conflicted
}

// conflictBlock absorbs every edit from either side that overlaps the region
// starting at (oEdits[oi], tEdits[ti]) and emits a marker block containing each
// side's content for that base range plus the base itself.
func conflictBlock(base []string, oEdits, tEdits []sideEdit, oi, ti int) (block []string, nextOI, nextTI, end int) {
	start := min(oEdits[oi].start, tEdits[ti].start)
	end = max(oEdits[oi].end, tEdits[ti].end)
	// Absorb further overlapping edits from both sides.
	for {
		changed := false
		for oi < len(oEdits) && oEdits[oi].start < end {
			end = max(end, oEdits[oi].end)
			oi++
			changed = true
		}
		for ti < len(tEdits) && tEdits[ti].start < end {
			end = max(end, tEdits[ti].end)
			ti++
			changed = true
		}
		if !changed {
			break
		}
	}
	oursTxt := applyEdits(base, oEdits, start, end)
	theirsTxt := applyEdits(base, tEdits, start, end)
	block = append(block, "<<<<<<< conflict (ours)")
	block = append(block, oursTxt...)
	block = append(block, "%%%%%%% base")
	for i := start; i < end && i < len(base); i++ {
		block = append(block, base[i])
	}
	block = append(block, "%%%%%%% side #1 (theirs)")
	block = append(block, theirsTxt...)
	block = append(block, ">>>>>>> conflict ends")
	return block, oi, ti, end
}

// applyEdits returns one side's content for the base range [start,end): base
// lines with that side's edits inside the range applied (deleted lines removed,
// inserted lines spliced in).
func applyEdits(base []string, edits []sideEdit, start, end int) []string {
	var out []string
	pos := start
	for _, e := range edits {
		if e.end <= start || e.start >= end {
			continue
		}
		lo := max(e.start, start)
		for pos < lo && pos < end {
			out = append(out, base[pos])
			pos++
		}
		out = append(out, e.lines...)
		pos = max(pos, e.end)
	}
	for pos < end && pos < len(base) {
		out = append(out, base[pos])
		pos++
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
