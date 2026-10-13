# Conflict Model

EasyVCS treats a merge conflict as a **first-class, content-addressed object**
rather than as working-tree state. This document describes the conflict model as
extended by `feat/conflict-model`: the `Term.Path` dimension, binary handling,
rename detection, rename conflicts, and the resolve extensions. It also explains
how these replace the text-marker approach used by the workspace gateway on top
of git.

## 1. The conflict object

A conflict is an N-way merge of one path's content, expressed as negative terms
(the base versions, `Removes`) and positive terms (the side versions, `Adds`):

```
object.Conflict{ Removes []Term, Adds []Term }
object.Term{ ID, Label, Path }
```

This mirrors jj's `Merge<T>`: a classic 3-way conflict is `[-base, +ours,
+theirs]`. An absent side is represented by a zero `ID` term so a resolution can
produce a deletion.

The conflict lives **in the tree** at the conflicted path as a `KindConflict`
entry. It is content-addressed and immutable, so it can be diffed, resolved, or
propagated to descendants without touching any working copy.

## 2. `Term.Path` (new)

`Term` gains a `Path string` field:

- **empty** — the legacy, path-less form. The term's content lives at the
  conflict's own path.
- **non-empty** — the term lives at a *different* path. This is what makes a
  rename conflict expressible: the same base blob reappears at different new
  paths on each side, and each side's path is recorded on its term.

### Backward-compatible encoding & hashing

Two canonical forms exist:

| form | magic | body |
|---|---|---|
| v1 (legacy) | `easyvcs-conflict-v1` | `count`, then `id`, `label` per term |
| v2 (paths)  | `easyvcs-conflict-v2` | `count`, then `id`, `label`, `path` per term |

`encodeConflictBytes` emits **v1 whenever no term has a non-empty Path**, so a
path-less conflict encodes — and therefore hashes — **byte-for-byte as before**.
`HashID`'s conflict branch shares the same body writer (`writeConflictForHash`),
so the hash and the stored encoding can never drift.

The result: existing conflict object ids in every store are stable. This is
pinned by `TestConflictPathlessHashStable`, which asserts a fixed sample keeps
its legacy id `e3dfe9aa…`.

`DecodeObject` / `IsConflictPayload` recognize both headers, so a v2 conflict
round-trips and an old v1 payload still decodes (with empty Paths).

## 3. Binary files

A blob is raw bytes; content addressing, transfer, and hashing are already
binary-safe. The only place binary content was mishandled was the **diff display
layer**, which ran a text LCS over arbitrary bytes.

- `revision.IsBinary(data)` reports a NUL byte within the first 8000 bytes (the
  same heuristic git uses).
- `FileDiff` gains a `Binary bool`. For added/removed/modified/renamed changes
  where either side is binary, the diff **does not** run `UnifiedDiff`: it sets
  `Binary=true`, writes a short note (`"Binary files a/<path> and b/<path>
  differ"`), and leaves `AddedLines`/`RemovedLines` at 0.

Binary *merging* needs no special case: `merge.Trees` compares sides by blob
**id**, so two sides editing the same binary produce a conflict object whose
terms carry both binary blobs (plus the base). Where a text merge is impossible,
the conflict is still a faithful, resolvable record — strictly better than git,
which can only offer "ours or theirs" for binary.

## 4. Rename detection (diff layer)

`object.Tree` has no rename concept — only add/delete. Rename detection is a
**diff-layer** concern, opt-in and deterministic:

- `revision.StatusRenamed` and `FileChange.OldPath` are added.
- `Workspace.DiffWithRenames(a, b)` pairs a removed base path with an added new
  path **only when their blob id is identical** — no similarity heuristics, so
  the result is deterministic and cheap. `Diff` (without renames) is unchanged
  and remains the default.
- `DiffContentFromChanges` renders a rename as `a/OldPath -> b/Path`; a rename
  that also changed content gets hunks, a pure rename gets none.

## 5. Rename conflicts (merge layer)

The per-path merge loop cannot see that the *same base blob* reappeared at
*different new paths*, so it would leave both new paths as clean adds (or, for
rename/modify, emit a plain content conflict at the base path). `merge.Trees`
now runs a second pass, `detectRenameConflicts`, that recognizes three cases:

| case | base `f=X` | ours | theirs | result |
|---|---|---|---|---|
| rename/rename | | `g=X` | `h=X` (`g!=h`) | conflict at `f`, terms at `g`/`h` |
| rename/modify | | `g=X` | `f=Y` | conflict at `f`, terms at `g`/`f` |
| modify/rename | | `f=Y` | `h=X` | conflict at `f`, terms at `f`/`h` |

For each, the new-path clean adds are removed and a single **path-bearing**
conflict is placed at the base path `f`. Any first-pass atom at `f` is dropped
so exactly one conflict is reported. A one-sided rename (the other side
unchanged) and a rename to the *same* path on both sides are **not** conflicts
and are left as clean merges.

## 6. Resolve extensions

`Resolve(revisionID, path, sideIndex)` (unchanged) picks one add term by index.
Two new resolvers cover what side-selection cannot:

- `ResolveToContent(revisionID, path, content []byte)` — writes arbitrary bytes
  as a new blob at `path`. This is the **manual-merge** escape hatch, essential
  for binary conflicts. The blob is content-addressed, so identical resolutions
  dedupe.
- `ResolveToPath(revisionID, path, chosenPath)` — for a rename conflict, picks
  **which side's path wins**: the chosen term's content is placed at
  `chosenPath`, and the conflict path plus every other side's path is removed, so
  the conflict settles on exactly one location.

All resolvers share one code path (`resolveApply`): they rewrite the conflict in
the revision's current tree and propagate the *same* resolution to descendants
that still carry the identical conflict object, keeping the revision id stable.

## 7. Replacing the gateway's text-marker scheme

The workspace gateway runs on Forgejo/git, whose conflicts are working-tree
state invisible to a server-side tool. To make conflicts agent-visible it writes
a sentinel marker (the literal string `ABCP` + `-CONFLICT`) **into the file
contents** and treats "does the file contain the sentinel" as the unresolved
signal (`internal/gitmerge/marker.go`). That approach:

- pollutes the file content and the commit tree,
- needs a custom sentinel scan to detect "unresolved",
- forces a custom diff3 and a custom rename policy because go-git offers neither,
- and can only binary-resolve by taking one whole side.

The first-class conflict model replaces all of it:

| gateway hack | easyvcs native |
|---|---|
| sentinel marker text written into files | `object.Conflict` stored as a tree entry |
| sentinel scan to detect "unresolved" | `Workspace.ConflictsInTree` / conflict atoms |
| custom diff3 | `merge.Trees` (N-way, path-aware) |
| implicit rename policy | explicit rename conflicts with `Term.Path` |
| binary: ours-or-theirs only | `ResolveToContent` (arbitrary merged bytes) |

Because the conflict is a real object, a resolver (human or agent) can diff it,
pick a side, choose a rename target, or write merged bytes — with no text
sentinel and no reliance on working-tree state.
