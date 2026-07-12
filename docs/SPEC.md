# spor — Specification

A versioning tool for creative coding workflows.

---

## 1. Overview

`spor` records a project's evolution automatically and lets the user return to
any previous state instantly. It should feel like an **infinite, automatic undo
history**, not a version control system — no commits, branches, staging, or
repositories to think about.

The single abstraction the user works with is an immutable **state**: the
complete contents of the project at one moment.

Experience:

- The user starts `spor` on a project directory; it watches the filesystem
  continuously.
- Every change, once activity settles, becomes a new state automatically. The
  user never commits.
- Restoring an old state is one command.
- Editing a restored state simply starts a new timeline — no branch is created or
  exposed.

Internally this forms a tree of states, but that is an implementation detail. The
system favors **simplicity over Git compatibility**, and optimizes for
**single-user experimentation**, not collaboration.

---

## 2. Data Model

### State

A state is immutable in content and contains:

- **id** — an opaque **ULID** (see below)
- **timestamp** — wall clock, for display only
- **parent** — a single parent state (an editable foreign key; the root has none)
- **manifest** — a map of every tracked file path to its blob hash
- **manifest hash** — stored for fast equality checks

Once created, a state's manifest and blobs never change. Its parent link *may* be
changed by explicit history operations (§5).

### Three identifiers, three jobs

| Identifier | Value | Purpose |
|---|---|---|
| **State ID** | opaque ULID | Names a state. Deliberately **not** derived from content or parent, so prune/compact can re-parent states without cascading new IDs down the subtree (the Git-rebase problem). |
| **Manifest hash** | `SHA-256` of the canonical manifest (sorted `path → blob_hash`) | Detects whether project *contents* changed (drives no-op suppression). |
| **Blob hash** | `SHA-256(content)` | Content-addresses file contents. This is where **deduplication** lives. |

Only *states* are opaque; *blobs* stay content-addressed. A ULID is unique and
time-sortable (its timestamp prefix sorts chronologically). `created_at` is a
separate column, not part of the ID.

### Topology

Every state has exactly one parent, so history is a **tree** (a forest if
multiple roots exist). Multi-parent merges are out of scope. A single persisted
**HEAD** row marks the current working state:

- new states descend from `HEAD`, and creating one advances it (same
  transaction);
- restore, prune, and compact move `HEAD` as described in §5.

`HEAD` is what makes "edit a restored state → new timeline" work: after a restore,
the next state descends from the restored state, not the previous tip.

---

## 3. Storage

### Metadata — SQLite (WAL mode)

Stores state rows (id, timestamp, parent, manifest hash), manifests, the `HEAD`
pointer, and room for future metadata (tags, previews).

### File contents — content-addressed blobs on disk

Stored as loose objects named by `SHA-256(content)`, separate from SQLite so the
database stays small and transactional while the object store scales to large
media. Blobs are:

- **immutable** — written once, never modified;
- **compressed** with Zstandard on disk (the hash is always over the *plaintext*;
  compression never affects identity);
- **streamed** — large media is hashed and compressed without loading into
  memory.

### Deduplication via the manifest

A state stores no file contents — only its manifest of `path → blob hash`.
Unchanged files keep the same hash, so they cost nothing in a new state; identical
contents anywhere map to one blob automatically. Deletions are a path's absence
from the manifest; renames are delete + add, with content dedup meaning nothing is
re-stored.

> **Tradeoff:** blobs are whole-file, so a one-pixel PNG edit re-stores the whole
> file. Accepted for v1. Content-defined chunking is the clean upgrade path for
> media dedup and doesn't disturb the rest of the model. Whole blobs (vs delta
> chains) are also what keep prune/compact/GC simple — a state's data is never
> entangled with a neighbor's.

---

## 4. Recording

New states are created automatically by watching the filesystem.

### The walk is the source of truth, not events

`spor` uses native notifications (fsnotify), but **events are only a trigger.**
They are lossy — buffers overflow, atomic saves and renames fire confusingly,
some editors emit nothing clean. So on each settle, `spor` **walks the whole
project tree**, hashes every tracked file, and rebuilds the manifest from what is
actually on disk. This makes deletions, renames, and missed events fall out for
free.

### Processing pipeline

```
fs events ──► "dirty" signal ──► debounce timer ──► [ snapshot job ] ──► single worker
 (noisy)      (something          (resets per event;   (at most ONE       (serial: walk,
              changed)            fires after quiet)    pending)           hash, write, commit)
```

- **Debounce and the serial worker solve different problems.** Debounce decides
  *when* the project is consistent to snapshot (files fully written, a multi-file
  change complete); the single worker ensures snapshots run *one at a time*,
  never racing the SQLite writer. Both are required.
- **At most one pending job.** A job carries no payload — it always means "walk
  and reconcile to disk *now*," so two pending jobs are redundant. Coalesce to
  one (a dirty flag / capacity-1 slot); never enqueue one job per event.
- **The dirty flag closes the walk-to-idle race.** A change landing after the
  worker finished walking but before it goes idle was *not* captured, yet a naive
  "skip if busy" check assumes it was. So any event during the whole job window
  sets `dirty`, and the worker checks it **atomically as it goes idle**, re-running
  if set. Nothing is ever silently missed.
- **Max-debounce cap.** A pure quiet-timer never fires during a continuous writer
  (a long render). Cap it to snapshot at least every M seconds so long streams
  still produce intermediate states.

Settle window: short enough to feel instant (~200–500 ms), long enough to outlast
an atomic-save burst or a project-wide save-all.

### No-op suppression

After the walk, if the new manifest hash equals `HEAD`'s, no state is created.
This absorbs mtime touches, saves-in-place, and sub-second edit-then-revert
fumbles without flooding history.

### Watch mechanics & ignore rules

- On Linux, inotify watches are **per-directory**: walk and add watches for every
  subdirectory, and for new directories as they appear.
- Always exclude `spor`'s own storage directory (avoids recursive events).
- Ignoring is mandatory — creative projects emit gigabytes of derived artifacts.
  Ignore editor temp/swap files (`*.tmp`, `*~`, `.DS_Store`, `4913`) and
  user-supplied, gitignore-style patterns (build output, caches, `node_modules`,
  exported media).

---

## 5. Operations

All operations produce or remove whole states. History editing (prune, compact)
is **destructive but never rewriting**: it removes states from the tree but never
alters what a surviving state contains. Both rely on opaque State IDs (no ID
cascade on re-parent) and on GC (§8) to reclaim now-unreferenced blobs.

### Restore

Materialize a state's working directory exactly and set `HEAD` to it. Because
recording is debounced, restore **force-settles first** so an in-flight edit
isn't lost:

1. drain the pending debounce timer and run the normal walk → create-state path;
2. materialize the target state's blobs into the working directory;
3. set `HEAD` to the restored state.

Restore never modifies existing states, and the pre-restore edit survives as its
own state — so restore is itself undoable.

### Apply

Cherry-pick the changes represented by one state onto the current working state,
producing a new state with a **single** parent (`HEAD`). No merge commits, no
multiple parents — the tree topology is preserved.

### Prune — delete a state and its whole subtree

1. If `HEAD` is inside the subtree, move it to the target's **parent** and
   re-materialize (force-settle first). Pruning the root deletes all history —
   require explicit confirmation.
2. Delete the subtree's rows in one transaction.
3. GC sweep reclaims newly-unreferenced blobs.

### Compact — squash a linear range into one state

Given ancestor `A` and descendant `B`:

1. Require the range **linear** — no intermediate has a child outside the range;
   otherwise refuse (reparenting side-branches is out of scope for v1).
2. Create `C` with `content(C) = content(B)`, `parent(C) = parent(A)`.
3. Reattach `B`'s children to `C`; if `HEAD` was in the range, set it to `C`.
4. Delete `A`…`B` in one transaction; GC sweep.

Intermediate snapshots are intentionally lost; only the start boundary
(`parent(A)`) and final contents (`C`) survive.

### Diffs

Not stored. Computed on demand by comparing blobs — a text diff when both are
text, a coarse added/changed/removed report otherwise. May be cached later if
profiling demands.

---

## 6. CLI & UX

The command surface is deliberately small and **undo-flavored**, not Git-flavored.
There is no `commit` (recording is automatic), no `branch` (branching is
implicit), and no `reset`/`discard` (nothing is ever lost, so there is nothing to
discard). Anything framed as "working dir vs current state" is a dead concept:
the daemon auto-snapshots within the settle window, so the working tree is
continuously kept identical to `@`.

### Referring to a state — `<ref>`

| Ref | Means |
|---|---|
| `@` | the current state (HEAD) |
| `@~n` | `n` states back along `@`'s ancestor line |
| `01ARZ7` | short ULID prefix |
| `mylabel` | a state the user named |
| `2h ago`, `yesterday`, `"friday 3pm"` | a time (the word `ago` is optional) |

Trailing positional args are joined into the ref, so `spor restore 2h ago` works
without quoting. A bare token is resolved in this precedence:

1. `@` / `@~n` — explicit sigils
2. exact **label** match (a label named `2h` wins over the duration)
3. parses as a **time**
4. **ULID prefix**

**Time rewinds `@`'s own timeline**, not the whole tree: `2h ago` finds the
ancestor of `@` that was current ~2h ago, never some abandoned branch.

`@` is only useful as an *operand that names the current state* (`label @ …`,
`compact @~5 @`, `prune @`, and the implicit "to now" end of a diff). `restore @`
and any working-dir diff are no-ops and are not use cases.

### Commands

**Everyday** (nearly all usage):

| Command | Effect |
|---|---|
| `spor start` / `spor stop` | begin / end watching this directory (starts/stops the daemon) |
| `spor log` | show the timeline as a **tree** (branches visible), newest first, marking `@` |
| `spor undo [n]` / `spor redo [n]` | step back / forward `n` states |
| `spor restore <ref>` | jump to any state |

`redo` is intentionally simple: it follows the **most-recently-visited child**.
Because editing after an `undo` starts a new branch (the old "future" is never
lost), other branches are reached via `spor log` + `restore`, not `redo`.

**Naming & inspecting:**

| Command | Effect |
|---|---|
| `spor label <ref> <name>` | name a state for easy reference |
| `spor diff <ref>` | changes from `<ref>` **to `@`** ("what's changed since then") |
| `spor diff <a> <b>` | changes between two states |
| `spor status` | daemon state and where `@` is |

Diff always compares **two points in history**; it never diffs against the
working tree.

**History editing** (occasional, destructive):

| Command | Effect |
|---|---|
| `spor prune <ref>` | delete a state **and all its descendants**; HEAD moves to its parent |
| `spor compact <a> <b>` | squash the linear range `a`…`b` into one state |

`prune` and `undo` look identical when `@` is a leaf but are not the same —
`undo` is a reversible cursor move, `prune @` **destroys** the state:

| | HEAD goes to | The state | Reversible |
|---|---|---|---|
| `spor undo` | parent | stays in history | yes (`redo`) |
| `spor prune @` | parent | destroyed (blobs GC'd) | no |

Because `prune` deletes a whole subtree: on a **leaf** `@` it drops just that one
state (the "rewind and delete the last state" case); on a **non-leaf** `@` (after
an undo/restore without editing) it drops the entire forward branch; on the
**root** it wipes all history. `prune` should feel heavy — confirm destructive
cases and report exactly what will be destroyed.

**Sync** (optional — see §7):

| Command | Effect |
|---|---|
| `spor push` / `spor pull` | sync states and blobs with the server |
| `spor remote add <url>` | configure the server |
| `spor remote prune <ref>` | delete a subtree **on the server** (sync is otherwise additive-only) |

**Maintenance** (rare; GC is mostly automatic):

| Command | Effect |
|---|---|
| `spor verify` | integrity check (see §8) |
| `spor gc` | reclaim unreferenced blobs |

---

## 7. Sync

Optional single-user **push/pull** to a server, purely for backup and moving
history between the same user's machines. **No collaboration** — one author, no
concurrent-editor merges, no conflict resolution. This is what keeps it simple;
the hard parts of sync don't exist here.

Opaque ULIDs don't undermine it: they are globally unique (no cross-machine
collisions), divergence between machines is just another branch in the tree the
model already supports, and blobs still dedup by content on the server (states are
tiny, so not deduping them costs nothing).

**The server is dumb** — a content-addressed blob store plus a table of state
rows, single-token auth (an object store + small index works equally well):

```
HEAD/PUT/GET  /blobs/<sha256>     blob exists? / upload / download
GET           /states             list state IDs the server has
PUT/GET       /states/<ulid>      upload / download a state (metadata + manifest)
```

- **Push:** diff local state IDs against the server's; for each missing state,
  upload its blobs **first**, then the state row — blobs before referencing
  states, parents before children (the local integrity invariant, on the wire).
- **Pull:** the mirror image, parents-first.
- The missing-set step is a plain ID set-difference; start naive (exchange the
  full set), optimize with a cursor later if needed.
- `HEAD` is **local and per-machine**, never synced as authoritative.

**Sync is additive-only** — it never deletes. To make the server forget a subtree,
prune it there explicitly (`spor remote prune <id>`); otherwise a later pull
re-downloads a locally-pruned state. Upside: the server doubles as a full archive.
Tombstones are out of scope for v1.

---

## 8. Runtime & Integrity

Correctness is prioritized over performance. Existing states must never become
corrupted; only the single state being created during a crash may be lost.

### Process model

The **watcher daemon owns everything** — the SQLite writer, the filesystem
watches, and the debounce timer. CLI commands (`spor restore`, `spor apply`,
`spor push`, …) are thin clients routing through it. This lets the daemon drain
its pending timer before acting (see Restore) and avoids multi-writer
coordination (WAL allows many readers, one writer). A command running without a
live daemon takes an exclusive lockfile.

### Write ordering & atomicity

- **Blob:** write temp → fsync → atomic rename to its content-addressed path →
  verify `SHA-256`.
- **State:** only after *all* its blobs are written and verified is the state row
  and `HEAD` advance committed to SQLite, in one transaction. An incomplete state
  is never visible; blobs from an abandoned state are harmless orphans.

### Crash recovery

On startup: remove abandoned temp files; incomplete state creations are
automatically discarded (nothing was committed); leftover blobs are orphans;
verify basic consistency; resume watching.

### Garbage collection

Part of v1, since prune/compact leave blobs unreferenced (and "infinite undo"
grows without bound). GC is a **mark-sweep** over blobs reachable from all
surviving states, run after every prune/compact and available as a command. A
blob is never treated as unreferenced without a full reachability pass, and
sweeping only ever deletes blobs — never state rows — so it can never corrupt a
surviving state.

### Verification

A command checks: every referenced blob exists and matches its `SHA-256`; every
manifest is well-formed and its stored hash recomputes; every `parent` and `HEAD`
resolves to a real state; and the parent graph is acyclic.

---

## 9. Design Principles

- Automatic by default — no manual commits, no visible branches, no staging.
- State *contents* are immutable; history may be explicitly pruned or compacted,
  never silently rewritten.
- Content-addressed blob storage (whole blobs, not delta chains).
- Events trigger; the filesystem walk is the source of truth.
- Instant restoration.
- Favor simplicity over Git compatibility; optimize for single-user
  experimentation, not collaboration.
