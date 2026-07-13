# spor: Specification

Infinite undo for your whole project. Jump back to any state, or branch off to
explore. Built for creative workflows.

---

## 1. Overview

`spor` records a project's evolution automatically and lets the user return to
any previous state instantly. It should feel like an **infinite, automatic undo
history**, not a version control system: no commits, branches, staging, or
repositories to think about.

The single abstraction the user works with is an immutable **state**: the
complete contents of the project at one moment.

Experience:

- States are created by a single **snapshot** operation, triggered either
  manually (`spor snapshot`) or automatically by a watcher that runs while
  `spor start` is open. Either way the user never writes a commit message or
  stages anything.
- Recording happens *while `spor` is running*, in the foreground; closing it
  stops watching. There is no hidden background daemon.
- Restoring an old state is one command.
- Editing a restored state simply starts a new timeline; no branch is created
  or exposed.

Internally this forms a tree of states, but that is an implementation detail.
The system favors **simplicity over Git compatibility**, and optimizes for
**single-user experimentation**, not collaboration.

---

## 2. Data Model

### State

A state is immutable in content and contains:

- **id**, an opaque **ULID** (see below)
- **timestamp**, wall clock; used for display and for time-based refs (§6)
- **parent**, a single parent state (an editable foreign key; the root has none)
- **manifest**, a map of every tracked file path to its blob hash plus one
  preserved permission bit (executable)
- **manifest hash**, stored for fast equality checks
- **label**, an optional user-given name (§6); mutable metadata, part of no hash

Once created, a state's manifest and blobs never change. Its parent link *may*
be changed by explicit history operations (§5).

### Three identifiers, three jobs

| Identifier | Value | Purpose |
|---|---|---|
| **State ID** | opaque ULID | Names a state. Deliberately **not** derived from content or parent, so prune/compact can re-parent states without cascading new IDs down the subtree (the Git-rebase problem). |
| **Manifest hash** | `SHA-256` of the canonical manifest (sorted `path → blob_hash → exec`) | Detects whether project *contents* changed (drives no-op suppression). |
| **Blob hash** | `SHA-256(content)` | Content-addresses file contents. This is where **deduplication** lives. |

Only *states* are opaque; *blobs* stay content-addressed. A ULID is unique and
time-sortable (its timestamp prefix sorts chronologically); `created_at` is
stored as its own column rather than parsed back out of the ID.

### What is tracked

Only **regular files**. Symbolic links, sockets, devices, and other special
files are skipped (symlink support is deferred). Empty directories are not
represented (a manifest is a map of file paths), and file mtimes are neither
stored nor restored.

### File permissions

Each manifest entry stores one permission bit, owner-execute, and it is part of
the manifest hash, so a bare `chmod +x` with no content change records a new
state. It is the only mode bit that alters behavior (scripts) and the only one
that stays portable across machines (which sync would expose); full Unix modes,
ownership, ACLs, and extended attributes are deliberately out of scope. This
mirrors Git, which records only `100644` vs `100755` for files. The blob hash
stays content-only, so the bit never affects deduplication.

### Topology

Every state has exactly one parent, so history is a **tree** (a forest if
multiple roots exist). Multi-parent merges are out of scope. A single persisted
**HEAD** row marks the current working state:

- new states descend from `HEAD`, and creating one advances it (same
  transaction);
- restore, prune, and compact move `HEAD` as described in §5.

`HEAD` is what makes "edit a restored state → new timeline" work: after a
restore, the next state descends from the restored state, not the previous tip.

Every `HEAD` move (snapshot, restore, undo, redo, prune, compact) is also
appended to a small **HEAD journal** (`head_history`: state id + timestamp).
The journal is what gives `redo` its meaning ("return to the state I just
left"). It is purely additive metadata: the tree never depends on it, and it
stays local (§7).

---

## 3. Storage

### Project root and implicit init

The store lives in a `.spor/` directory at the project root:

```
.spor/
  spor.db            SQLite (WAL)
  blobs/ab/<rest>    zstd-compressed, content-addressed objects, fanned
                     out git-style by the hash's first two hex chars so
                     no single directory grows unbounded
  tmp/               staging for temp → rename
  write.lock         advisory write lock (§8)
  watcher.lock       advisory watcher lock (§8)
```

There is no `init` command: the first `snapshot` (or `spor start`) creates this
layout implicitly. Commands find the project root by walking up from the
working directory to the nearest `.spor/`, the way Git finds `.git/`, so
running from a subdirectory operates on the whole project instead of creating a
nested store. Implicit creation is guarded: spor refuses to create a store
directly in the filesystem root or the user's home directory, so a stray
command cannot start snapshotting an enormous tree.

> **Cloud-synced folders:** creative projects often live in Dropbox / Drive /
> iCloud folders. Exclude `.spor/` from such sync: a live SQLite database
> inside a file syncer is a known corruption vector, and the blob store churns
> on every snapshot. Moving the store outside the project tree (e.g.
> `~/.local/share/spor/<project-id>`) is a possible future direction.

### Metadata: SQLite (WAL mode)

Stores state rows (id, timestamp, parent, manifest hash, optional label),
manifests, the `HEAD` pointer, the HEAD journal (§2), the stat cache (§4), and
room for future metadata (previews, tags).

### File contents: content-addressed blobs on disk

Stored as loose objects named by `SHA-256(content)`, separate from SQLite so
the database stays small and transactional while the object store scales to
large media. Blobs are:

- **immutable**, written once, never modified;
- **compressed** with Zstandard on disk (the hash is always over the
  *plaintext*; compression never affects identity);
- **streamed**, large media is hashed and compressed without loading into
  memory.

### Deduplication via the manifest

A state stores no file contents, only its manifest of `path → blob hash`.
Unchanged files keep the same hash, so they cost nothing in a new state;
identical contents anywhere map to one blob automatically. Deletions are a
path's absence from the manifest; renames are delete + add, with content dedup
meaning nothing is re-stored.

> **Tradeoff:** blobs are whole-file, so a one-pixel PNG edit re-stores the
> whole file. Accepted for v1. Content-defined chunking is the clean upgrade
> path for media dedup and doesn't disturb the rest of the model. Whole blobs
> (vs delta chains) are also what keep prune/compact/GC simple: a state's data
> is never entangled with a neighbor's.

---

## 4. Recording

### The snapshot operation

Every state is created by one core operation, **snapshot**, regardless of
trigger: **walk the whole tree**, hash every tracked file (the stat cache below
elides re-reading unchanged ones), write any new blobs, create a state under
`HEAD`, advance `HEAD`. It rebuilds the manifest from what is actually on disk
rather than trusting file events (which are lossy, dropped on buffer overflow,
scrambled by atomic saves), so deletions, renames, and missed events fall out
for free.

Two triggers call it:

- **Manual**: `spor snapshot` runs it once and exits. spor is fully usable this
  way with no watcher (a deliberate, git-like rhythm; also what makes it
  scriptable and testable).
- **Automatic**: the watcher calls it when the filesystem settles (below).

**No-op suppression** is part of the operation: if the new manifest hash equals
`HEAD`'s, no state is created. So repeated snapshots with nothing changed,
mtime touches, saves-in-place, and sub-second edit-then-revert fumbles all
record nothing.

### Ignoring files

The walk excludes files that should not be versioned, resolved in this order:

- **`.spor/` is always excluded** and cannot be re-included; it is spor's own
  store, and versioning it would be self-referential.
- **Built-in defaults**: the `.git/` directory (high-churn, tool-owned, and
  meaningless to version) and common editor/OS temp files (`*.tmp`, `*~`,
  `*.swp`, `*.swo`, `.DS_Store`, `4913`).
- **`.sporignore`**: an optional file at the project root using full gitignore
  syntax (globs, `**`, anchoring, directory-only `foo/`, `#` comments, and `!`
  negation). It is layered after the defaults, so a project can re-include a
  default (e.g. `!keep.tmp`).

Matched directories are pruned wholesale (e.g. `node_modules/`), never walked.
`.sporignore` is itself tracked, like `.gitignore`, and spor never creates it
(it is opt-in). Nested per-directory ignore files are out of scope for v1.

### The stat cache

Hashing every file makes snapshot cost proportional to project size, which the
watcher would pay on every settle. The store therefore keeps a **stat cache**:
for every path in the last snapshot, `(size, mtime_ns, inode) → blob hash`,
plus the time the row was recorded. The walk still enumerates every file and
remains the source of truth for existence, deletions, and renames; the cache
only elides *re-reading contents*:

- a file whose size, mtime, and inode all match its row reuses the recorded
  blob hash without being opened (the blob's presence on disk is still
  verified, one stat);
- any mismatch, or a missing row, reads and rehashes the file and refreshes the
  row;
- **racily clean**: a row whose file mtime is not older than the row's own
  recording time is never trusted; such files are always rehashed, since an
  edit inside the filesystem's timestamp granularity could otherwise be missed.
  This is the same defense Git's index uses;
- on platforms without a usable inode (Windows), matching uses size + mtime
  only.

Rows are written in the same transaction as the state they describe; a
suppressed no-op snapshot still refreshes the cache (in a transaction of its
own), so a cold cache warms even when nothing changed. The cache is advisory: a
stale or missing row only costs a rehash, and the racily-clean rule closes the
one case where a matching row could lie.

### Vanishing files

A file that vanishes between enumeration and reading (editor atomic-save temp
files do this constantly) is treated as deleted, exactly as if the walk had
never seen it. This race is routine while a watcher is running and never aborts
a snapshot.

Any other failure (an unreadable file or directory, an I/O error) aborts the
snapshot with an error naming the offending path: fix it or exclude it via
`.sporignore`. Nothing partial is ever recorded. Carrying locked-but-present
paths over from `HEAD`, which Windows file locking will eventually want, is
deferred.

### The execute bit

The walk observes the execute bit from the filesystem, so `chmod +x` is
captured like any other change (§2). On platforms that cannot report it
(Windows), the bit is **inherited from the parent state** instead: observing
would read every file as non-executable and, because the bit is part of the
manifest hash, flip inherited scripts off as a spurious state. New files there
default to non-executable, and setting the bit needs an explicit command
(deferred). This is the same tradeoff as Git's `core.fileMode`.

### The watcher: automatic triggering

While `spor start` runs, a watcher turns filesystem activity into snapshots
through one serial pipeline:

```
fs events ──► "dirty" signal ──► debounce timer ──► [ snapshot job ] ──► single worker
 (noisy)      (something          (resets per event;   (at most ONE       (serial: walk,
              changed)            fires after quiet)    pending)           hash, write, commit)
```

- **Debounce vs the serial worker, different jobs.** Debounce decides *when*
  the project is consistent to snapshot (files fully written, a multi-file
  change complete); the single worker ensures snapshots run one at a time.
  Both required.
- **At most one pending job.** A job means only "reconcile to disk now," so two
  are redundant; coalesce to one (a dirty flag / capacity-1 slot), never one
  job per event.
- **The dirty flag closes the walk-to-idle race.** A change landing after the
  worker walked but before it goes idle wasn't captured, yet "skip if busy"
  would assume it was. So any event during the job sets `dirty`, checked
  atomically as the worker goes idle; if set, it re-runs. Nothing is silently
  missed.
- **Max-debounce cap.** A pure quiet-timer never fires during a continuous
  writer (a long render), so cap it to snapshot at least every M seconds. A
  capped snapshot may therefore capture in-progress (torn) files; accepted:
  the next settle records the consistent version, and history keeps both.

Settle window: instant-feeling (~200-500 ms) but long enough to outlast an
atomic-save burst or a project-wide save-all.

### Watch mechanics

- On Linux, inotify watches are **per-directory**: walk and add watches for
  every subdirectory, and for new directories as they appear.
- The watcher does not watch `spor`'s own storage directory (avoids recursive
  events).
- The same ignore rules the walk applies (above) keep derived artifacts, which
  creative projects emit in bulk, from triggering snapshots.

---

## 5. Operations

All operations produce or remove whole states. History editing (prune, compact)
is **destructive but never rewriting**: it removes states from the tree but
never alters what a surviving state contains. Both rely on opaque State IDs (no
ID cascade on re-parent) and on GC (§8) to reclaim now-unreferenced blobs.

### Restore

Materialize a state's working directory exactly and set `HEAD` to it. Because
recording is debounced, restore **force-settles first** so an in-flight edit
isn't lost. A one-shot `spor restore` cannot drain another process's debounce
timer, so force-settling means restore performs a snapshot itself:

1. under the write lock, run the normal walk → create-state path (a no-op if
   nothing changed);
2. materialize the target state: write every file in its manifest (applying
   the stored execute bit), and delete every path that is in `HEAD`'s manifest
   but not in the target's. Paths outside `HEAD`'s manifest, untracked or
   ignored (`.git/`, `node_modules/`, build artifacts), are **never touched**;
3. set `HEAD` to the restored state (journal appended).

The watcher's next settle then sees the restored tree and records nothing
(no-op suppression). Restore never modifies existing states, and the
pre-restore edit survives as its own state, so restore is itself undoable.

Restore is not atomic: a crash mid-materialization leaves a mixed working tree.
Recovery is re-running the restore; nothing was lost, since step 1 already
recorded the pre-restore tree.

### Apply

Cherry-pick one state's changes onto the current state: compute the delta
`diff(parent(ref), ref)` (a parentless `ref` counts as a delta from empty, i.e.
pure additions) and replay it onto `HEAD`'s contents, producing a new state
with a **single** parent (`HEAD`). No merge commits, no multiple parents; the
tree topology is preserved.

Each path in the delta resolves three-way, with *base* = its version in
`parent(ref)`, *theirs* = in `ref`, *ours* = in `HEAD`:

- *ours* == *base* (unchanged here): take *theirs*, including deletions;
- *ours* == *theirs* (both sides agree): nothing to do;
- both changed, both text: **diff3 merge**; a clean merge takes the merged
  content, overlapping hunks write standard conflict markers into the file and
  the apply is reported as conflicted. Nothing is lost: the pre-apply state
  still exists, and resolving the markers is just editing toward the next
  state;
- both changed, at least one side binary: the applied state's version wins,
  reported;
- modify/delete (deleted on one side, modified on the other): the surviving
  modified file is kept, reported.

### Prune: delete a state and its whole subtree

1. If `HEAD` is inside the subtree, move it to the target's **parent** and
   re-materialize (force-settle first). Pruning the root deletes all history;
   require explicit confirmation.
2. Delete the subtree's rows in one transaction.
3. GC sweep reclaims newly-unreferenced blobs.

### Compact: squash a linear range into one state

Given ancestor `A` and descendant `B`:

1. Require the range **linear**, no intermediate has a child outside the range;
   otherwise refuse (reparenting side-branches is out of scope for v1).
2. Create `C` with `content(C) = content(B)`, `parent(C) = parent(A)`.
3. Reattach `B`'s children to `C`; if `HEAD` was in the range, set it to `C`.
4. Delete `A`…`B` in one transaction; GC sweep.

Intermediate snapshots are intentionally lost; only the start boundary
(`parent(A)`) and final contents (`C`) survive.

### Diffs

Not stored. Computed on demand by comparing blobs: a text diff when both are
text, a coarse added/changed/removed report otherwise. May be cached later if
profiling demands.

---

## 6. CLI & UX

The command surface is deliberately small and **undo-flavored**, not
Git-flavored. There is no `commit` (recording is automatic), no `branch`
(branching is implicit), and no `reset`/`discard` (nothing is ever lost, so
there is nothing to discard). Anything framed as "working dir vs current state"
is a dead concept: while `spor start` is watching, snapshots happen within the
settle window, so the working tree is continuously kept identical to `@`.

### Referring to a state: `<ref>`

| Ref | Means |
|---|---|
| `@` | the current state (HEAD) |
| `@~n` | `n` states back along `@`'s ancestor line |
| `01ARZ7` | short ULID prefix |
| `mylabel` | a state the user named |
| `2h ago`, `yesterday`, `"friday 3pm"` | a time (the word `ago` is optional) |

Trailing positional args are joined into the ref, so `spor restore 2h ago`
works without quoting. A bare token is resolved in this precedence:

1. `@` / `@~n`, explicit sigils
2. exact **label** match (a label named `2h` wins over the duration)
3. parses as a **time**
4. **ULID prefix**

**Time rewinds `@`'s own timeline**, not the whole tree: a time `T` resolves to
the deepest ancestor of `@` created at or before `T`, never some abandoned
branch. Creation times strictly increase along any ancestor chain, so this is
well defined even after a restore to an old state.

`@` is only useful as an *operand that names the current state* (`label @ …`,
`compact @~5 @`, `prune @`, and the implicit "to now" end of a diff).
`restore @` and any working-dir diff are no-ops and are not use cases.

### Commands

**Everyday** (nearly all usage):

| Command | Effect |
|---|---|
| `spor start` | run the watcher in the foreground with a **live log** of the tree building itself; Ctrl+C stops watching |
| `spor snapshot [-m <label>]` | create one state now, then exit; the watcher-free, scriptable path |
| `spor log` | show the timeline as a **tree** (branches visible), newest first, marking `@` |
| `spor undo [n]` / `spor redo [n]` | step back / forward `n` states |
| `spor restore <ref>` | jump to any state |

`spor start` is, for v1, a live monitor only: it shows states appearing, the
settle indicator, and where `@` is. A full interactive TUI (navigating and
driving restore/prune/label from within it) is deferred; until then, mutations
are one-shot CLI commands.

`redo` is intentionally simple: it follows the **most-recently-visited child**
of `@`, as recorded by the HEAD journal (§2). Because editing after an `undo`
starts a new branch (the old "future" is never lost), other branches are
reached via `spor log` + `restore`, not `redo`.

**Naming & inspecting:**

| Command | Effect |
|---|---|
| `spor label <ref> <name>` | name a state for easy reference |
| `spor diff <ref>` | changes from `<ref>` **to `@`** ("what's changed since then") |
| `spor diff <a> <b>` | changes between two states |
| `spor status` | whether a watcher is running and where `@` is |

Diff always compares **two points in history**; it never diffs against the
working tree.

**History editing** (occasional, destructive):

| Command | Effect |
|---|---|
| `spor prune <ref>` | delete a state **and all its descendants**; HEAD moves to its parent |
| `spor compact <a> <b>` | squash the linear range `a`…`b` into one state |

`prune` and `undo` look identical when `@` is a leaf but are not the same:
`undo` is a reversible cursor move, `prune @` **destroys** the state:

| | HEAD goes to | The state | Reversible |
|---|---|---|---|
| `spor undo` | parent | stays in history | yes (`redo`) |
| `spor prune @` | parent | destroyed (blobs GC'd) | no |

Because `prune` deletes a whole subtree: on a **leaf** `@` it drops just that
one state (the "rewind and delete the last state" case); on a **non-leaf** `@`
(after an undo/restore without editing) it drops the entire forward branch; on
the **root** it wipes all history. `prune` should feel heavy: confirm
destructive cases and report exactly what will be destroyed.

**Sync** (optional, see §7):

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
history between the same user's machines. **No collaboration**: one author, no
concurrent-editor merges, no conflict resolution. This is what keeps it simple;
the hard parts of sync don't exist here.

Opaque ULIDs don't undermine it: they are globally unique (no cross-machine
collisions), divergence between machines is just another branch in the tree the
model already supports, and blobs still dedup by content on the server (states
are tiny, so not deduping them costs nothing).

**The server is dumb**, a content-addressed blob store plus a table of state
rows, single-token auth (an object store + small index works equally well):

```
HEAD/PUT/GET  /blobs/<sha256>     blob exists? / upload / download
GET           /states             list state IDs the server has
PUT/GET       /states/<ulid>      upload / download a state (metadata + manifest)
```

- **Push:** diff local state IDs against the server's; for each missing state,
  upload its blobs **first**, then the state row. Blobs before referencing
  states, parents before children: the local integrity invariant, on the wire.
- **Pull:** the mirror image, parents-first.
- The missing-set step is a plain ID set-difference; start naive (exchange the
  full set), optimize with a cursor later if needed.
- `HEAD` is **local and per-machine**, never synced as authoritative; so are
  the HEAD journal and the stat cache. Only states and blobs travel.

**Sync is additive-only**, it never deletes. To make the server forget a
subtree, prune it there explicitly (`spor remote prune <id>`); otherwise a
later pull re-downloads a locally-pruned state. Upside: the server doubles as a
full archive. Tombstones are out of scope for v1.

---

## 8. Runtime & Integrity

Correctness is prioritized over performance. Existing states must never become
corrupted; only the single state being created during a crash may be lost.

### Process model: core engine + front-ends

There is **no daemon.** All behavior lives in a UI-agnostic **core engine** (a
Go package) owning the operations (snapshot, restore, apply, prune, compact,
gc, diff, log, label, verify), ref resolution, and locking. Three unprivileged
front-ends call it:

- **One-shot CLI** (`spor snapshot`, `spor restore`, …): open store, call one
  op, exit.
- **The watcher** (`spor start`): a foreground process whose debounce timer
  calls `snapshot` on settle, alongside the live log. Ctrl+C stops it.
- **A future TUI**: interactive keys calling the same ops.

### Locking

Three layers, no process management:

1. **SQLite's own locking** protects the DB file: WAL gives many readers plus
   one writer, and a second writer waits (not errors) with
   `PRAGMA busy_timeout`. Necessary but not sufficient: a snapshot writes blobs
   before its transaction and a restore materializes files outside any
   transaction, so it can't serialize whole operations or the `HEAD`
   read-modify-write.

2. **Two advisory file locks** (`flock(2)`; in Go `github.com/gofrs/flock`,
   with `LockFileEx` on Windows), whose decisive property is that the kernel
   releases them on process exit, *including crash or `SIGKILL`*, so there are
   no stale locks to clean up. The files (`.spor/write.lock`,
   `.spor/watcher.lock`) are empty; contents are only a `spor status`
   diagnostic.
   - **Write lock**, held by the core for the *duration of each mutating
     operation*, so all front-ends serialize; reads never take it (so
     `log`/`diff`/`status` always work). Being per-operation, a one-shot
     `spor restore` runs *while* `spor start` watches: they serialize, the
     restore completes under the lock (force-settle included), and the
     watcher's next settle sees the restored tree as a no-op. Acquired
     blocking with a short timeout.
   - **Watcher lock**, held by `spor start` for its lifetime, so a project has
     at most one watcher. Acquired non-blocking, so a second `spor start`
     fails immediately.

3. **Atomic file replacement** (temp → `fsync` → rename) for blobs, the
   pattern Git uses for its `*.lock` files. Not a mutex; it makes individual
   writes atomic. Blob writes need no write lock: content-addressed
   temp+rename is idempotent, so concurrent identical writes are harmless.

Avoided: `O_CREAT|O_EXCL` process lockfiles (stale on `SIGKILL`, forcing
liveness checks) and holding a long SQLite transaction as the app lock.
Advisory locks are unreliable on network filesystems, but SQLite already
requires a local one.

### Write ordering & atomicity

- **Blob:** write temp → fsync → atomic rename to its content-addressed path →
  **fsync the containing directory**, so the rename itself survives power loss
  (directory fsync is skipped on Windows, which cannot sync directory
  handles) → verify `SHA-256`.
- **State:** only after *all* its blobs are written, verified, and their
  directories fsynced is the state row and `HEAD` advance committed to SQLite,
  in one transaction. Otherwise a power loss could persist the SQLite commit
  while losing a rename, leaving a state that references a missing blob. An
  incomplete state is never visible; blobs from an abandoned state are
  harmless orphans.

### Crash recovery

Whenever the store is opened, recovery runs first: remove abandoned temp files;
incomplete state creations are automatically discarded (nothing was committed);
leftover blobs are orphans; verify basic consistency. Only then does the caller
(a one-shot command or `spor start`) proceed.

### Schema versioning

The store carries a plain integer schema version (the highest applied
migration), readable even by a binary that does not know newer migrations. On
open, it is compared against the version embedded in the binary:

- **store older**: migrate up, after backing up the DB file (`VACUUM INTO`, a
  clean single-file copy with no WAL to reconcile);
- **equal**: no-op;
- **store newer**: refuse with "upgrade spor". A binary never writes into a
  store from the future.

### Garbage collection

Part of v1, since prune/compact leave blobs unreferenced (and "infinite undo"
grows without bound). GC is a **mark-sweep** over blobs reachable from all
surviving states, run after every prune/compact and available as a command. GC
takes the write lock like any other mutating operation, so it can never race an
in-flight snapshot (whose blobs land on disk before its state row commits). A
blob is never treated as unreferenced without a full reachability pass, and
sweeping only ever deletes blobs, never state rows, so it can never corrupt a
surviving state.

### Verification

A command checks: every referenced blob exists and matches its `SHA-256`; every
manifest is well-formed and its stored hash recomputes; every `parent` and
`HEAD` resolves to a real state; and the parent graph is acyclic.

---

## 9. Design Principles

- Automatic by default: no manual commits, no visible branches, no staging.
- State *contents* are immutable; history may be explicitly pruned or
  compacted, never silently rewritten.
- Content-addressed blob storage (whole blobs, not delta chains).
- Events trigger; the filesystem walk is the source of truth.
- Instant restoration.
- Favor simplicity over Git compatibility; optimize for single-user
  experimentation, not collaboration.
