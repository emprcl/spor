# spor: Design Specification

`spor` records a project's contents over time and lets the user return to any
previous state. This document specifies the data model, storage, recording,
operations, CLI, and runtime design.

Some capabilities described here are not yet built; they are marked
_(not yet implemented)_ inline, and §11 lists them together.

---

## 1. Overview

`spor` records a project's evolution automatically and lets the user return to
any previous state. The model is an automatic undo history rather than a version
control system: there are no commits, branches, staging, or repositories to
manage.

The single abstraction the user works with is an immutable **state**: the
complete contents of the project at one moment.

Experience:

- States are created by a single **snapshot** operation, triggered
  automatically by a watcher that runs while something is watching (`spor ui`
  in watch mode, or `spor watch`), or manually (`spor snap`) when nothing is.
  Either way the user never writes a commit message or stages anything.
- Recording happens *while `spor` is running*, in the foreground; closing it
  stops watching. There is no hidden background daemon.
- Restoring an old state is one command.
- Editing a restored state simply starts a new timeline; no branch is created
  or exposed.

Internally this forms a tree of states, but that is an implementation detail.
The design favors simplicity over Git compatibility and targets single-user use,
not collaboration.

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
- **label**, an optional user-given name (§6), unique across states like the id
  (unlabeled states are simply absent from that namespace); mutable metadata,
  part of no hash

Once created, a state's manifest and blobs never change. Its parent link *may*
be changed by explicit history operations (§5).

### Three identifiers, three jobs

| Identifier | Value | Purpose |
|---|---|---|
| **State ID** | opaque ULID | Names a state. Deliberately **not** derived from content or parent, so drop/trim/fold can re-parent states without cascading new IDs down the subtree (the Git-rebase problem). |
| **Manifest hash** | `SHA-256` of the canonical manifest (sorted `path → blob_hash → exec`) | Detects whether project *contents* changed (drives no-op suppression). |
| **Blob hash** | `SHA-256(content)` | Content-addresses file contents. This is where **deduplication** lives. |

Only *states* are opaque; *blobs* stay content-addressed. A ULID is unique and
time-sortable (its timestamp prefix sorts chronologically); `created_at` is
stored as its own column rather than parsed back out of the ID.

### What is tracked

Only **regular files**. Symbolic links, sockets, devices, and other special
files are skipped (symlink support is _not yet implemented_). Empty directories are not
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
- go, drop, trim, and fold move `HEAD` as described in §5.

`HEAD` is what makes "edit a restored state → new timeline" work: after a
restore, the next state descends from the restored state, not the previous tip.

Every `HEAD` move (snap, go, undo, redo, drop, trim, fold) is also
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

There is no `init` command: the first `snapshot` (or `spor watch`) creates this
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

> **Tradeoff:** blobs are whole-file, so any change re-stores the whole file: a
> one-pixel PNG edit, a re-encoded video, a re-saved JPEG. Accepted for v1.
> Content-defined chunking (CDC) is the upgrade path and doesn't disturb the
> rest of the model: chunks are just smaller content-addressed blobs, so dedup,
> GC's mark-sweep, and the stat cache all generalize to them. Whole chunks (vs
> delta chains) also keep drop/trim/fold/GC simple: a state's data is never
> entangled with a neighbor's.
>
> **CDC is a bounded, format-dependent win, not a general answer to large
> media.** It only recovers dedup that survives in the *bytes*, i.e. when
> unchanged regions stay byte-identical across an edit. It pays off for
> **appended/growing files** (screen and audio captures, renders that append),
> which dedup near-perfectly; **uncompressed or framed streams** edited locally
> (WAV region edits, RAW, EXR, ID3 tag prepends); and **ZIP-container project
> formats** (Sketch, Procreate, `.docx`), whose untouched entries dedup when the
> app doesn't rewrite the whole archive. It buys almost nothing when a small
> logical change rewrites the whole byte stream: a re-encoded video or MP3, a
> re-saved JPEG, or, because compression destroys locality, the very one-pixel
> PNG above. For those cases only snapshot *policy* (size thresholds, fewer
> snapshots of huge files) helps, not chunking. Note also that CDC is a storage
> optimization, not a capability: spor already versions binary files today, as
> opaque whole blobs.

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

- **Automatic**: the watcher calls it when the filesystem settles (below).
  This is the everyday path.
- **Manual**: `spor snap` runs it once and exits. spor is fully usable this
  way with no watcher (a deliberate, git-like rhythm; also what makes it
  scriptable and testable).

Snapshot reports **progress** through an optional callback in four phases —
scanning the tree, storing content, the whole-store durability sync, and the
commit — so a front-end can keep a large first snapshot from being a silent
wait (`spor snap` and `spor watch` draw a progress bar on a terminal; the TUI
shows an indexing panel).

**No-op suppression** is part of the operation: if the new manifest hash equals
`HEAD`'s, no state is created. So repeated snapshots with nothing changed,
mtime touches, saves-in-place, and sub-second edit-then-revert fumbles all
record nothing.

### Ignoring files

The walk excludes files that should not be versioned, resolved in this order:

- **`.spor/` is always excluded** and cannot be re-included; it is spor's own
  store, and versioning it would be self-referential.
- **Built-in defaults**: the `.git/` directory (high-churn, tool-owned, and
  meaningless to version), common editor/OS temp files (`*.tmp`, `*~`,
  `*.swp`, `*.swo`, `.DS_Store`, `4913`), and the usual huge derived
  directories (`node_modules/`, `build/`, `dist/`, `target/`, `__pycache__/`,
  `.venv/`), so a first snapshot never sweeps them in.
- **`.sporignore`**: an optional file at the project root using full gitignore
  syntax (globs, `**`, anchoring, directory-only `foo/`, `#` comments, and `!`
  negation). It is layered after the defaults, so a project can re-include a
  default (e.g. `!keep.tmp`).

Matched directories are pruned wholesale (e.g. `node_modules/`), never walked.
A project that really does keep sources under a defaulted name re-includes it
with a negation (e.g. `!build/`).
`.sporignore` is itself tracked, like `.gitignore`, and spor never creates it
(it is opt-in). Nested per-directory ignore files are out of scope.

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
_not yet implemented_.

### The execute bit

The walk observes the execute bit from the filesystem, so `chmod +x` is
captured like any other change (§2). On platforms that cannot report it
(Windows), the bit is **inherited from the parent state** instead: observing
would read every file as non-executable and, because the bit is part of the
manifest hash, flip inherited scripts off as a spurious state. New files there
default to non-executable; setting the bit on Windows is out of scope. This is
the same tradeoff as Git's `core.fileMode`.

### The watcher: automatic triggering

While `spor watch` runs, a watcher turns filesystem activity into snapshots
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

**Recording starts with a baseline.** The moment watching begins (`spor watch`
startup, or the TUI's watch mode turning on), one snapshot runs immediately, so
the session always starts from a recorded point; no-op suppression makes it
free when nothing changed since the last state.

Settle window: short (300 ms by default) but long enough to outlast an
atomic-save burst or a project-wide save-all; the max-debounce cap defaults to
5 s.

### Watch mechanics

- On Linux, inotify watches are **per-directory**: walk and add watches for
  every subdirectory, and for new directories as they appear.
- The watcher does not watch `spor`'s own storage directory (avoids recursive
  events).
- The same ignore rules the walk applies (above) keep derived artifacts, which
  creative projects emit in bulk, from triggering snapshots.

---

## 5. Operations

All operations produce or remove whole states. History editing (drop, trim,
fold, thin) is **destructive but never rewriting**: it removes states from the tree
but never alters what a surviving state contains. They all rely on opaque State IDs
(no ID cascade on re-parent) and on GC (§8) to reclaim now-unreferenced blobs.

### Go

Materialize a state's working directory exactly and set `HEAD` to it. Because
recording is debounced, `go` **force-settles first** so an in-flight edit
isn't lost. A one-shot `spor go` cannot drain another process's debounce
timer, so force-settling means `go` takes a snapshot itself:

1. under the write lock, run the normal walk → create-state path (a no-op if
   nothing changed);
2. materialize the target state: write every file in its manifest (applying
   the stored execute bit), and delete every path that is in `HEAD`'s manifest
   but not in the target's. Paths outside `HEAD`'s manifest, untracked or
   ignored (`.git/`, `node_modules/`, build artifacts), are **never touched**;
3. set `HEAD` to the target state (journal appended).

The watcher's next settle then sees the materialized tree and records nothing
(no-op suppression). `go` never modifies existing states, and the edit made
just before it survives as its own state, so `go` is itself undoable.

`go` is not atomic: a crash mid-materialization leaves a mixed working tree.
Recovery is re-running it; nothing was lost, since step 1 already recorded the
pre-existing tree.

### Pick: bring one path back

`spor pick <ref> <path>` is the file-level counterpart of `go`: it writes
one file (or every file under one directory) out of a past state into the
working tree, and nothing else. `HEAD` does not move, no other path is touched,
and nothing is ever deleted. Like `go` it force-settles first, so the
pre-pick tree is recorded; the picked result is then recorded as a new
state, so a pick is itself undoable.

### Drop: delete a state and its whole subtree

1. If `HEAD` is inside the subtree, move it to the target's **parent** and
   re-materialize (force-settle first). Dropping from the root deletes all history;
   require explicit confirmation.
2. Delete the subtree's rows in one transaction.
3. GC sweep reclaims newly-unreferenced blobs.

### Trim: make a state the new root, dropping the rest

The dual of drop: where drop deletes a state's subtree, trim keeps **only**
that subtree. Given target `S`, the survivors are `S` and its descendants;
everything else, `S`'s ancestors and any side branches hanging off them, is
dropped, and `S` becomes a root. This is how a long project forgets old history
and reclaims its space while keeping everything from a chosen point forward.

1. Force-settle first, so an in-flight edit isn't lost.
2. If `HEAD` is not among the survivors (you were on a branch being dropped),
   relocate it to `S` and re-materialize; otherwise leave it in place.
3. Set `parent(S) = NULL`, making `S` a root.
4. Delete every non-survivor state in one transaction, children before parents so
   the `parent_id` foreign key is never violated.
5. GC sweep reclaims newly-unreferenced blobs.

Like drop it is destructive but never rewriting: no surviving state's contents
change, only `S`'s parent link. Trimming to an existing root is a no-op.
`trim` confirms and reports exactly what will be destroyed.

### Fold: squash a linear range into one state

Given ancestor `A` and descendant `B`:

1. Require the range **linear**, no intermediate has a child outside the range;
   otherwise refuse (reparenting side-branches is out of scope for v1).
2. Create `C` with `content(C) = content(B)`, `parent(C) = parent(A)`.
3. Reattach `B`'s children to `C`; if `HEAD` was in the range, set it to `C`.
4. Delete `A`…`B` in one transaction; GC sweep.

Intermediate snapshots are intentionally lost; only the start boundary
(`parent(A)`) and final contents (`C`) survive.

### Thin: reduce history to its skeleton

The persistent form of the hiding `spor log` already does at display time (§6):
where fold squashes one range you name, thin collapses **every** linear run at
once, across the whole tree. It keeps only the structurally significant states,
every **tip** (no children) and **branch point** (two or more children), plus
every **labeled** state and **`HEAD`** (the same states `log` never hides),
and drops the linear in-between states.

1. Compute the keep-set (tips, branch points, labels, `HEAD`); everything else,
   an unlabeled non-`HEAD` state with exactly one child, is dropped.
2. Reparent each survivor whose parent is dropped onto its nearest surviving
   ancestor (or to a root when the whole chain above it is dropped).
3. Delete the dropped states in one transaction, children before parents; GC sweep.

Because `HEAD` is always kept, thin never moves `HEAD` and never touches the
working tree, so unlike drop/trim/fold it needs no force-settle. Like them it is
destructive but never rewriting: no surviving state's contents change, only
which states exist and the survivors' parent links. It is idempotent (a second
run finds only tips and branch points, plus labels and `HEAD`, and drops
nothing). A purely linear history collapses to its latest state.

### Forget: remove the store entirely

Delete the whole `.spor/` store, every state and blob, and stop tracking the
project. Unlike drop/trim/fold,
which edit the tree but keep the store and your files, `forget` operates on the
store as a whole and leaves nothing behind to reclaim.

1. Refuse if a `spor watch` is running (the watcher lock is held): stop watching
   first, so nothing races the removal.
2. Confirm, reporting how much will be destroyed (state count, on-disk size).
   This is irreversible: there is no GC pass or surviving state to recover from.
3. Close the database and remove the `.spor/` directory wholesale.

Working files are **never touched**, only `.spor/`. Afterwards the project is
untracked again, and the next `snap` or `spor watch` creates a fresh store
from scratch (§3, implicit init).

### Diffs

Not stored. Computed on demand by comparing blobs: a text diff when both are
text, a coarse added/changed/removed report otherwise. May be cached later if
profiling demands.

---

## 6. CLI & UX

The command surface is deliberately small and modeled on undo, not Git. There
is no `commit` (recording is automatic), no `branch`
(branching is implicit), and no `reset`/`discard` (nothing is ever lost, so
there is nothing to discard). While something is watching (`spor ui` in watch
mode, or `spor watch`), snapshots happen within the settle window, so the
working tree is continuously kept identical to `@` and "working dir vs current
state" has no meaning. In the manual rhythm (no watcher, `spor snap` by hand)
the tree *can* drift from `@`; the idiom for "what have I changed since the
last snapshot?" is to snap first, then diff:
`spor snap && spor diff @~1`. Diff itself never reads the working tree (below).

### Referring to a state: `<ref>`

| Ref | Means |
|---|---|
| `@` | the current state (HEAD) |
| `@~n` | `n` states back along `@`'s ancestor line |
| `01ARZ7` | short ULID prefix |
| `mylabel` | a state the user named |
| `2h`, `3d` | a duration back from now |

A ref is always a single argument (a label containing spaces must be quoted).
A bare token is resolved in this precedence:

1. `@` / `@~n`, explicit sigils
2. exact **label** match (a label named `2h` wins over the duration)
3. parses as a **time**
4. **ULID prefix**

A time is a duration back from now, in seconds, minutes, hours, or days
(`s`, `m`, `h`, `d`). Calendar dates are out of scope.

**Time rewinds `@`'s own timeline**, not the whole tree: a time `T` resolves to
the deepest ancestor of `@` created at or before `T`, never some abandoned
branch. Creation times strictly increase along any ancestor chain, so this is
well defined even after a restore to an old state.

`@` is only useful as an *operand that names the current state* (`label @ …`,
`fold @~5 @`, `drop @`, and the implicit "to now" end of a diff).
`go @` and any working-dir diff are no-ops and are not use cases.

### Commands

**Interactive**:

| Command | Effect |
|---|---|
| `spor ui` | open the interactive TUI (below); it offers to watch on startup, and watching is toggled from inside it |

**Everyday** (nearly all usage):

| Command | Effect |
|---|---|
| `spor watch` | record in the foreground, streaming one line per snapshot; the headless recorder for a spare terminal or a redirected log; Ctrl+C stops watching |
| `spor snap [-l <label>]` | create one state now, then exit; the manual, scriptable path, only needed when nothing is watching |
| `spor log` | show the history newest-first as **swimlanes**: each branch keeps its own column, and long linear runs are hidden down to their most recent few; marks `@` |
| `spor undo [n]` / `spor redo [n]` | step back / forward `n` states (clamped to the history boundary) |
| `spor go <ref>` | jump to any state |
| `spor pick <ref> <path>` | bring one file (or directory) back from a state, without moving `@` or touching anything else |

### The interactive TUI (`spor ui`)

`spor ui` is the interactive front-end: the same swimlane tree as `spor log`,
navigable, with a detail panel for the selected snapshot (its identity, label,
timing, lineage, and the actions available on it; the panel drops away on a
narrow terminal). The layout is a one-line status bar (watch/browse indicator,
project path, history and store size, where `@` sits, and transient
results/errors), the body, and a one-line key bar; `?` overlays the full key
reference. Navigation is the arrow keys or `j`/`k`, `g`/`G` for top and
bottom, and the mouse wheel. It needs a terminal; `spor log` is the printable
fallback.

New snapshots appear live at the top; a cursor left on the first row keeps
tracking the newest snapshot as they land. The view also stays honest against
*other* processes: a 1 s tick probes SQLite's `data_version` and reloads only
when the store actually changed, so a mutation from another terminal shows up
within a second without the tick paying for reads.

Watching is a **mode inside it**, not a separate command: on startup, if no
watcher holds the lock, it asks whether to watch (when another process is
already watching, the offer is skipped and the status bar says so); `--watch`
and `--browse` pick the startup mode up front and skip that offer. The `w`
key toggles watching at any time (acquiring and releasing the watcher lock,
§8), and the status bar always shows which mode it is in. Turning watching on
records the baseline snapshot immediately (§4), drawing the indexing progress
panel while a large first snapshot runs. When not watching, `s` records a
snapshot by hand (the key is disabled while watching, since the watcher
records).

Every mutation the TUI offers calls the same core operations as the one-shot
commands, on the *selected* snapshot: jump (`enter`, confirmed, since go
rewrites the working tree), diff (`d`, against the snapshot's **parent**: "what
did this snapshot change", shown full-screen and scrollable; the one-arg CLI
`diff <ref>` remains ref-to-`@`), label (`l`; submitting an empty value
removes an existing label), pick (`p`, a
live search over the files of the snapshot's manifest), drop (`x`) and trim
(`t`), both confirmed with the exact counts their plans report, undo/redo
(`u`/`r`), and quit (`q`, confirmed; it also stops watching).

The display-time hiding of long linear runs is interactive: a summary row
("*n* snaps hidden") expands with `→` and collapses with `←`, and `f` on a
summary row **folds** that run permanently (the `fold` operation on exactly
the hidden range, confirmed). `T` runs `thin` (confirmed with its plan). The
wording is deliberate: the harmless display state is always "hidden", and
"fold" always means the destructive operation, so the two cannot be confused.

`redo` is intentionally simple: it follows the **most-recently-visited child**
of `@`, as recorded by the HEAD journal (§2). Because editing after an `undo`
starts a new branch (the old "future" is never lost), other branches are
reached via `spor log` + `go`, not `redo`.

Both **clamp** rather than error: asking to step further than history allows lands
on the oldest (or newest-visited) state and reports how far it moved, matching the
undo metaphor. Both are `go` under the hood, so each force-settles first (an
uncommitted edit survives as a branch) and each is itself reversible.

**Naming & inspecting:**

| Command | Effect |
|---|---|
| `spor label <ref> <name>` | name a state for easy reference (labels are unique); bare `spor label` lists them; `spor label -d <name>` removes one |
| `spor diff <ref>` | changes from `<ref>` **to `@`** ("what's changed since then") |
| `spor diff <a> <b>` | changes between two states |
| `spor status` | project path, whether a watcher is running, history size (snap and timeline counts), on-disk store size, and where `@` sits (on a tip, or how many newer states are ahead) |

Diff always compares **two points in history**; it never diffs against the
working tree.

**History editing** (occasional, destructive):

| Command | Effect |
|---|---|
| `spor drop <ref>` | delete a state **and all its descendants**; HEAD moves to its parent |
| `spor trim <ref>` | make a state the new root, dropping everything **not** under it (the dual of drop) |
| `spor fold <a> <b>` | squash the linear range `a`…`b` into one state |
| `spor thin` | reduce the whole history to its tips, branch points, and labels, dropping the linear runs (`HEAD` and the working tree never move) |

`drop` and `undo` look identical when `@` is a leaf but are not the same:
`undo` is a reversible cursor move, `drop @` **destroys** the state:

| | HEAD goes to | The state | Reversible |
|---|---|---|---|
| `spor undo` | parent | stays in history | yes (`redo`) |
| `spor drop @` | parent | destroyed (blobs GC'd) | no |

Because `drop` deletes a whole subtree: on a **leaf** `@` it drops just that
one state (the "rewind and delete the last state" case); on a **non-leaf** `@`
(after an undo/go without editing) it drops the entire forward branch; on
the **root** it wipes all history. `drop` confirms destructive cases and
reports exactly what will be destroyed.

**Starting over** (destructive, removes the whole store):

| Command | Effect |
|---|---|
| `spor forget` | delete the entire `.spor/` store, all history and blobs; working files are left untouched |

`forget` removes the store rather than editing the tree (§5): every state and
blob is gone and the project is no longer tracked (the next `snap` or
`spor watch` starts fresh).
It refuses while a `spor watch` is running, and because it is irreversible it
confirms and reports how much will be deleted. It never touches your working
files, only `.spor/`.

**Sync** (optional, see §7), _not yet implemented_:

| Command | Effect |
|---|---|
| `spor push` / `spor pull` | sync states and blobs with the server |
| `spor remote add <url>` | configure the server |
| `spor remote drop <ref>` | delete a subtree **on the server** (sync is otherwise additive-only) |

**Attachments** (pin reference media to a state, see §9), _not yet implemented_:

| Command | Effect |
|---|---|
| `spor attach <ref> <path>...` | pin one or more files to a state (default `@`); copies their bytes now, records no state |
| `spor attachments <ref>` | list a state's attachments |
| `spor detach <id>` | remove one attachment |
| `spor export <id> [<out>]` | write an attachment's bytes back out |

**Maintenance** (rare; GC is mostly automatic):

| Command | Effect |
|---|---|
| `spor verify` | integrity check (see §8) |
| `spor gc` | reclaim unreferenced blobs |

---

## 7. Sync

> _Not yet implemented (planned)._ This section describes a future capability;
> none of the `push` / `pull` / `remote` commands or the server exist yet (§11).

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
subtree, run `drop` there explicitly (`spor remote drop <id>`); otherwise a
later pull re-downloads a state you dropped locally. Upside: the server doubles as a
full archive. Tombstones are out of scope for v1.

---

## 8. Runtime & Integrity

Correctness is prioritized over performance. Existing states must never become
corrupted; only the single state being created during a crash may be lost.

### Process model: core engine + front-ends

There is **no daemon.** All behavior lives in a UI-agnostic **core engine** (a
Go package) owning the operations (snap, go, drop, trim,
fold, thin, gc, diff, log, label, verify, forget), ref resolution, and locking. Three unprivileged
front-ends call it:

- **One-shot CLI** (`spor snap`, `spor go`, …): open store, call one
  op, exit.
- **The watcher** (`spor watch`): a foreground process whose debounce timer
  calls `snap` on settle, streaming one line per recorded state. Ctrl+C stops it.
- **The TUI** (`spor ui`, §6): interactive keys calling the same ops; it runs
  the same watcher pipeline in-process when watching is toggled on, holding the
  watcher lock exactly as `spor watch` does.

### Locking

Three layers, no process management:

1. **SQLite's own locking** protects the DB file: WAL gives many readers plus
   one writer, and a second writer waits (not errors) with
   `PRAGMA busy_timeout`. Necessary but not sufficient: a snap writes blobs
   before its transaction and a go materializes files outside any
   transaction, so it can't serialize whole operations or the `HEAD`
   read-modify-write.

2. **Two advisory file locks** (`flock(2)`; in Go `github.com/gofrs/flock`,
   with `LockFileEx` on Windows), whose decisive property is that the kernel
   releases them on process exit, *including crash or `SIGKILL`*, so there are
   no stale locks to clean up. The files (`.spor/write.lock`,
   `.spor/watcher.lock`) are empty; contents are only a `spor status`
   diagnostic.
   - **Write lock**, held by the core for the *duration of each mutating
     operation* (including `forget`, so the store is never deleted under an
     in-flight write), so all front-ends serialize; reads never take it (so
     `log`/`diff`/`status` always work). Being per-operation, a one-shot
     `spor go` runs *while* `spor watch` watches: they serialize, the
     `go` completes under the lock (force-settle included), and the
     watcher's next settle sees the restored tree as a no-op. Acquired
     blocking with a short timeout.
   - **Watcher lock**, held by `spor watch` for its lifetime, so a project has
     at most one watcher. Acquired non-blocking, so a second `spor watch`
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
  harmless orphans. SQLite runs with `PRAGMA synchronous=FULL`: WAL's default
  (NORMAL) may lose the most recent commit on power loss, and that commit is
  exactly the state-row-plus-`HEAD` advance the blob fsyncs just paid for.

### Crash recovery

Whenever the store is opened, recovery runs first. Abandoned temp files are
removed. Incomplete state creations need no cleanup: a state row and its `HEAD`
advance commit in one transaction, so a crash leaves either the whole state or
none, and blobs from an abandoned state are harmless orphans that GC reclaims.
A cheap structural check then runs (`HEAD` resolves, no dangling parent, and an
acyclic graph), and the store refuses to open if it fails, pointing at
`spor verify` for detail. The check reads no blobs; blob presence and hashes are
left to `verify`. `verify` and `forget` skip the check so they can act on a
damaged store. Only then does the caller (a one-shot command or `spor watch`)
proceed.

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

Part of the core, since the history edits (§5) leave blobs unreferenced (and history
grows without bound). GC is a **mark-sweep** over blobs reachable from all
surviving states, run after every drop, trim, fold, and thin, and available as
a command. GC
takes the write lock like any other mutating operation, so it can never race an
in-flight snap (whose blobs land on disk before its state row commits). A
blob is never treated as unreferenced without a full reachability pass, and
sweeping only ever deletes blobs, never state rows, so it can never corrupt a
surviving state.

### Verification

A command checks: every referenced blob exists and matches its `SHA-256`; every
manifest is well-formed and its stored hash recomputes; every `parent` and
`HEAD` resolves to a real state; and the parent graph is acyclic.

---

## 9. Attachments

> _Not yet implemented (planned)._ This section describes a future capability;
> the `attach` / `attachments` / `detach` / `export` commands and the
> `attachments` table do not exist yet (§11).

spor targets exploratory work, and users want to pin reference media, mainly
images (a screenshot of a generative sketch, a render, a reference photo), to a
state so a later front-end (web, TUI, GUI) can show *where they were* at that
moment. An **attachment** is commentary *about* a state, not part of its
content.

### Model

An attachment is a named binary object bound to one state and stored as
**mutable metadata, like a label** (§2): it is part of no hash, and adding or
removing one never creates, alters, or re-parents a state. That is exactly what
lets a user annotate a *past* state retroactively, which is the whole point of
the feature.

An `attachments` table carries: an **id** (ULID), the **state id** it belongs to
(a foreign key, `ON DELETE CASCADE`), a **blob hash**, a **name** (original
filename or caption), a **media type** (sniffed MIME, so a UI knows an
`image/png` from an `audio/wav`), and a **created_at** timestamp.

Attachments reuse the content-addressed blob store (§3): an image attached to
several states, or one identical to a tracked file already in the store, costs a
single blob. Because blobs already stream, compress, and deduplicate, large
media needs no new storage machinery.

### Capture semantics

`attach` **copies bytes at attach time; it is not a live link** to a file. It
reads the file now, content-addresses it into a blob, and records a row.
Editing or deleting the source afterwards leaves the attachment untouched: the
pinned image stays as it was when attached. This is the desired behavior for
"remember where I was," and the one thing worth stating explicitly in help text,
since a user might assume the opposite.

A file referenced from *inside* the watched project is captured the same way. If
it is tracked and unchanged, its bytes are already a blob, so the attachment
deduplicates to it at zero storage cost; if it is ignored or outside the
project, a new blob is stored and kept alive by the attachment row alone. Either
way the write lands only in `.spor/`, which the walk always excludes (§4), so
attaching never dirties the tree and never triggers a snapshot. The row insert
takes the write lock (§8) like any other mutation, so it serializes against a
running watcher's snapshot; the content-addressed blob write is idempotent, so a
concurrent snapshot of the same file just produces the identical object.

### Storage & integrity

Attachment rows live in SQLite; their bytes live in the blob store like any
other blob. Two integration points:

- **GC (§8)** adds attachment blobs to the reachable set, so an image is kept
  while *either* a manifest entry or an attachment references it. Dropping a
  state cascades its attachment rows; the next sweep reclaims any blob then
  referenced by nothing.
- **Verify (§8)** checks each attachment blob exists and matches its `SHA-256`,
  alongside manifest blobs.

### Sync

Attachments are durable metadata, so they travel with states over sync (§7),
unlike `HEAD`, the journal, and the stat cache, which stay local. An attachment
row is uploaded after its blob and its state (blobs before the rows that
reference them, parents before children), the same ordering the rest of sync
uses.

### CLI

| Command | Effect |
|---|---|
| `spor attach <ref> <path>...` | pin one or more files to the state named by `<ref>` (default `@`); copies their bytes now, moves no `HEAD`, records no state |
| `spor attachments <ref>` | list a state's attachments (id, name, type, size); a pure read |
| `spor detach <id>` | remove one attachment (its blob is GC'd if now unreferenced) |
| `spor export <id> [<out>]` | write an attachment's bytes back out, so the CLI can hand an image to a viewer before the TUI/GUI exist |

`<ref>` defaults to `@`, so `spor attach screenshot.png` pins to the current
state without the user ever handling an id.

---

## 10. Design Principles

- Automatic by default: no manual commits, no visible branches, no staging.
- State *contents* are immutable; history may be explicitly dropped or
  folded, never silently rewritten.
- Content-addressed blob storage (whole blobs, not delta chains).
- Events trigger; the filesystem walk is the source of truth.
- Restoration materializes a state directly, without replaying history.
- Favor simplicity over Git compatibility; target single-user use, not
  collaboration.

---

## 11. Implementation status

The core is implemented: recording (manual `snap` and the `watch` watcher), the
stat cache, ignore rules (built-in defaults plus root `.sporignore`), the
zstd-compressed content-addressed blob store, the state tree and HEAD journal,
`go` / `undo` / `redo`, `pick`, `drop` / `trim` / `fold` / `thin`, `label`, `diff`, `log`
(newest-first swimlanes with hidden linear runs), `status`, `verify`, `gc`,
`forget`, advisory file locking, schema versioning and migrations, crash-safe
write ordering with an on-open consistency check, and concurrent readers
alongside the single serialized writer. The **interactive TUI** (`spor ui`, §6)
is implemented: tree navigation, the watch mode with its startup offer and `w`
toggle, and in-view snap, go, diff, label/unlabel, pick (searchable), drop,
trim, fold-a-hidden-run, thin, and undo/redo.

The following are described above but **not yet implemented** (planned):

- **Sync** (§6, §7): `push`, `pull`, `remote add`, `remote drop`, and the
  server. None of it exists yet.
- **Attachments** (§9): pinning reference media to a state; the `attach` /
  `attachments` / `detach` / `export` commands and the `attachments` table.
- **Symlinks** (§2): only regular files are tracked.
- **Carrying locked-but-present paths on Windows** (§4).
- **Content-defined chunking** (§3) and **diff caching** (§5): performance
  upgrades, not yet needed.

Deliberately out of scope (not merely deferred): multi-parent merges, full Unix
modes / ownership / ACLs / xattrs, multi-user collaboration, calendar date
parsing, nested per-directory ignore files, and setting the execute bit on
Windows.
