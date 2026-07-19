package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
	"github.com/emprcl/spor/internal/remote"
	"github.com/emprcl/spor/internal/textfmt"
)

// Sync is optional single-user push/pull (docs/design-spec.md §7): backup, and
// moving history between one person's machines. There is no collaboration and no
// concurrent editing, which is what keeps it small.
//
// The store's two halves travel differently. Blobs hold all the bytes, so they
// stay content-addressed and additive. The state graph is tiny, so it is sent
// whole and versioned by a generation counter, and push is a compare-and-swap on
// that counter. Sending the graph whole is what lets a thin propagate: an id
// set-difference cannot express "this state was deleted" or "this one moved".
//
// Network I/O never happens under the write lock. A push of a large tree would
// otherwise block spor watch for its whole duration, so both operations read and
// transfer unlocked, then take the lock briefly to commit.

// ErrNoRemote is returned when an operation needs a configured server.
var ErrNoRemote = errors.New("no remote configured; run 'spor remote add <url>'")

// SyncPhase labels the stage a sync is in, for progress reporting.
type SyncPhase int

const (
	// SyncScan is reading local state and working out what must move.
	SyncScan SyncPhase = iota
	// SyncUpload is sending blobs.
	SyncUpload
	// SyncDownload is fetching blobs.
	SyncDownload
	// SyncApply is writing the merged graph to the store.
	SyncApply
)

// SyncOptions tunes a push or pull.
type SyncOptions struct {
	// Force overrides the safety stop. On push it overwrites the server's graph
	// even though it has moved on; on pull it settles conflicts in the server's
	// favor rather than reporting them.
	Force bool
	// OnProgress, when set, is called as work proceeds. Never nil internally.
	OnProgress func(phase SyncPhase, done, total int)
}

func (o SyncOptions) progress() func(SyncPhase, int, int) {
	if o.OnProgress == nil {
		return func(SyncPhase, int, int) {}
	}
	return o.OnProgress
}

// RemoteInfo describes the configured server.
type RemoteInfo struct {
	URL       string
	ProjectID string
	SyncedGen int64
	SyncedAt  time.Time
	HasToken  bool
}

// PushResult reports what a push moved.
type PushResult struct {
	NoOp          bool
	States        int
	BlobsUploaded int
	BytesUploaded int64
	Generation    int64
}

// PullResult reports what a pull changed.
type PullResult struct {
	NoOp            bool
	Added           int
	Removed         int
	Updated         int
	BlobsDownloaded int
	Resurrected     []string
	ForcedRemote    []string
	LabelsCleared   []LabelClear
	Generation      int64
}

// SyncConflictError reports states both machines edited differently. It is the
// deliberate stopping point: with one user and no concurrent editing, this means
// history was rewritten in two places, and guessing would be worse than asking.
type SyncConflictError struct {
	Conflicts []SyncConflict
}

// Error renders on a single line: the terminal front-end collapses newlines, so
// the message has to read as prose rather than as a list.
func (e *SyncConflictError) Error() string {
	parts := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		parts = append(parts, c.describe())
	}
	return fmt.Sprintf(
		"this machine and the server disagree about %d %s (%s). "+
			"Run 'spor pull --force' to take the server's history, "+
			"or 'spor push --force' to replace it with this machine's",
		len(e.Conflicts), textfmt.Plural(len(e.Conflicts), "snapshot", "snapshots"),
		strings.Join(parts, "; "))
}

// describe phrases one conflict in terms of what each machine did, rather than
// naming the internal field.
func (c SyncConflict) describe() string {
	id := textfmt.Abbrev(c.StateID)
	switch c.Field {
	case "deleted":
		if c.Local == "(deleted)" {
			return fmt.Sprintf("%s: deleted here, but the server %s it", id, c.Remote)
		}
		return fmt.Sprintf("%s: %s here, but the server deleted it", id, c.Local)
	case "label":
		return fmt.Sprintf("%s: named %s here, %s on the server",
			id, quoteOrNone(c.Local), quoteOrNone(c.Remote))
	default:
		return fmt.Sprintf("%s: moved under %s here, under %s on the server",
			id, abbrevOrRoot(c.Local), abbrevOrRoot(c.Remote))
	}
}

func quoteOrNone(s string) string {
	if s == "" {
		return "nothing"
	}
	return fmt.Sprintf("%q", s)
}

func abbrevOrRoot(id string) string {
	if id == "" {
		return "no parent"
	}
	return textfmt.Abbrev(id)
}

// blobBatchSize caps how many hashes go in one missing-blobs request.
const blobBatchSize = 500

// RemoteAdd configures the server. projectID names this project across machines:
// leave it empty on the first machine to mint one, and pass the id reported by
// 'spor remote' when setting up a second machine against the same project.
//
// Pointing the store at a different remote or project resets the sync base: the
// last-synced graph describes a relationship with one server, and means nothing
// against another.
func (e *Engine) RemoteAdd(ctx context.Context, rawURL, projectID, token string) (RemoteInfo, error) {
	// Reject a URL a token could never be filed under, before anything is stored.
	if _, err := credentialKey(rawURL); err != nil {
		return RemoteInfo{}, err
	}
	cfg, err := e.q.GetSync(ctx)
	if err != nil {
		return RemoteInfo{}, fmt.Errorf("reading sync config: %w", err)
	}

	if projectID == "" {
		projectID = cfg.ProjectID.String
		if projectID == "" {
			projectID = ulid.Make().String()
		}
	}
	// Validate the pair before storing it, so a bad URL fails here rather than at
	// the first push.
	if _, err := remote.New(rawURL, projectID, "", nil); err != nil {
		return RemoteInfo{}, err
	}

	changed := cfg.RemoteUrl.String != rawURL || cfg.ProjectID.String != projectID

	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return RemoteInfo{}, err
	}
	defer func() { _ = wl.Release() }()

	err = e.inTx(ctx, func(q *gen.Queries) error {
		if err := q.SetRemote(ctx, gen.SetRemoteParams{
			ProjectID: nullString(projectID),
			RemoteUrl: nullString(rawURL),
		}); err != nil {
			return fmt.Errorf("storing remote: %w", err)
		}
		if !changed {
			return nil
		}
		if err := q.ClearSyncBase(ctx); err != nil {
			return fmt.Errorf("clearing sync base: %w", err)
		}
		return q.SetSyncedGen(ctx, gen.SetSyncedGenParams{SyncedGen: 0})
	})
	if err != nil {
		return RemoteInfo{}, err
	}

	if token != "" {
		if err := saveToken(rawURL, token); err != nil {
			return RemoteInfo{}, err
		}
	}
	return e.Remote(ctx)
}

// RemoteForget removes the remote configuration and the sync base. It touches
// neither states nor blobs, here or on the server: to remove history from the
// server, delete it locally and push, since deletions propagate.
func (e *Engine) RemoteForget(ctx context.Context) error {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return err
	}
	defer func() { _ = wl.Release() }()

	return e.inTx(ctx, func(q *gen.Queries) error {
		if err := q.ClearSyncBase(ctx); err != nil {
			return fmt.Errorf("clearing sync base: %w", err)
		}
		return q.ClearRemote(ctx)
	})
}

// Remote reports the configured server, or ErrNoRemote.
func (e *Engine) Remote(ctx context.Context) (RemoteInfo, error) {
	cfg, err := e.q.GetSync(ctx)
	if err != nil {
		return RemoteInfo{}, fmt.Errorf("reading sync config: %w", err)
	}
	if !cfg.RemoteUrl.Valid || cfg.RemoteUrl.String == "" {
		return RemoteInfo{}, ErrNoRemote
	}
	info := RemoteInfo{
		URL:       cfg.RemoteUrl.String,
		ProjectID: cfg.ProjectID.String,
		SyncedGen: cfg.SyncedGen,
	}
	if cfg.SyncedAt.Valid {
		info.SyncedAt = time.UnixMilli(cfg.SyncedAt.Int64)
	}
	if tok, err := loadToken(cfg.RemoteUrl.String); err == nil && tok != "" {
		info.HasToken = true
	}
	return info, nil
}

// newClient builds a client from the stored configuration.
func (e *Engine) newClient(ctx context.Context) (*remote.Client, gen.GetSyncRow, error) {
	cfg, err := e.q.GetSync(ctx)
	if err != nil {
		return nil, cfg, fmt.Errorf("reading sync config: %w", err)
	}
	if !cfg.RemoteUrl.Valid || cfg.RemoteUrl.String == "" {
		return nil, cfg, ErrNoRemote
	}
	token, err := loadToken(cfg.RemoteUrl.String)
	if err != nil {
		return nil, cfg, err
	}
	cl, err := remote.New(cfg.RemoteUrl.String, cfg.ProjectID.String, token, e.httpClient)
	if err != nil {
		return nil, cfg, err
	}
	return cl, cfg, nil
}

// localGraph reads the current state graph in wire form.
func (e *Engine) localGraph(ctx context.Context) (map[string]remote.State, error) {
	rows, err := e.q.ListStatesForSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing states: %w", err)
	}
	g := make(map[string]remote.State, len(rows))
	for _, r := range rows {
		g[r.ID] = remote.State{
			ID:           r.ID,
			Parent:       r.ParentID.String,
			CreatedAt:    r.CreatedAt,
			ManifestHash: r.ManifestHash,
			Label:        r.Label.String,
		}
	}
	return g, nil
}

// baseGraph reads the last-synced graph.
func (e *Engine) baseGraph(ctx context.Context) (map[string]remote.State, error) {
	rows, err := e.q.ListSyncBase(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading sync base: %w", err)
	}
	g := make(map[string]remote.State, len(rows))
	for _, r := range rows {
		g[r.StateID] = remote.State{
			ID:           r.StateID,
			Parent:       r.ParentID.String,
			CreatedAt:    r.CreatedAt,
			ManifestHash: r.ManifestHash,
			Label:        r.Label.String,
		}
	}
	return g, nil
}

// writeBase replaces the sync base with states. Callers hold the write lock and
// supply a transaction.
func writeBase(ctx context.Context, q *gen.Queries, states map[string]remote.State) error {
	if err := q.ClearSyncBase(ctx); err != nil {
		return fmt.Errorf("clearing sync base: %w", err)
	}
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := states[id]
		if err := q.AddSyncBaseEntry(ctx, gen.AddSyncBaseEntryParams{
			StateID:      s.ID,
			ParentID:     nullString(s.Parent),
			CreatedAt:    s.CreatedAt,
			ManifestHash: s.ManifestHash,
			Label:        nullString(s.Label),
		}); err != nil {
			return fmt.Errorf("recording sync base entry %s: %w", id, err)
		}
	}
	return nil
}

// headPins returns HEAD and its ancestors, the states a pull must never delete.
// head is ON DELETE SET NULL and head_history is ON DELETE CASCADE, so letting
// the other machine's thin remove them would null HEAD and prune the journal.
func (e *Engine) headPins(ctx context.Context, local map[string]remote.State) (map[string]struct{}, error) {
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading HEAD: %w", err)
	}
	pins := make(map[string]struct{})
	if !head.Valid {
		return pins, nil
	}
	for cur := head.String; cur != ""; {
		s, ok := local[cur]
		if !ok {
			break
		}
		if _, seen := pins[cur]; seen {
			break // defensive: a cycle must not spin
		}
		pins[cur] = struct{}{}
		cur = s.Parent
	}
	return pins, nil
}

// manifestBytes renders a state's manifest into the canonical bytes that travel
// as a blob, and checks them against the state's recorded manifest hash. A
// mismatch means the store disagrees with itself, so it stops rather than
// uploading something mislabeled.
func (e *Engine) manifestBytes(ctx context.Context, stateID, wantHash string) ([]manifestEntry, []byte, error) {
	rows, err := e.q.ListManifestEntries(ctx, stateID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading manifest of %s: %w", stateID, err)
	}
	entries := make([]manifestEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, manifestEntry{path: r.Path, hash: r.BlobHash, exec: r.Executable != 0})
	}
	raw := serializeManifest(entries)
	if got := hashBytes(raw); got != wantHash {
		return nil, nil, fmt.Errorf(
			"state %s: manifest hashes to %s but the store records %s; run spor verify",
			stateID, got, wantHash)
	}
	return entries, raw, nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sortedStates returns the graph as a slice ordered by id, so the wire form of a
// given graph is always byte-identical.
func sortedStates(g map[string]remote.State) []remote.State {
	out := make([]remote.State, 0, len(g))
	for _, s := range g {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// graphsEqual reports whether two graphs are identical in every synced field.
func graphsEqual(a, b map[string]remote.State) bool {
	if len(a) != len(b) {
		return false
	}
	for id, sa := range a {
		sb, ok := b[id]
		if !ok || sa != sb {
			return false
		}
	}
	return true
}

// missingBlobs asks the server which hashes it lacks, in batches.
func missingBlobs(ctx context.Context, cl *remote.Client, hashes []string) (map[string]struct{}, error) {
	missing := make(map[string]struct{})
	for start := 0; start < len(hashes); start += blobBatchSize {
		end := min(start+blobBatchSize, len(hashes))
		batch, err := cl.MissingBlobs(ctx, hashes[start:end])
		if err != nil {
			return nil, err
		}
		for _, h := range batch {
			missing[h] = struct{}{}
		}
	}
	return missing, nil
}

// countingReader tallies the bytes streamed through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Push sends this machine's history to the server.
//
// The server's graph is replaced wholesale, guarded by a compare-and-swap on the
// generation: if the server has moved since this machine last synced, the push
// stops and asks for a pull, so another machine's work is never silently
// overwritten. Blobs go up before the graph that references them, which is the
// local blobs-before-states invariant put on the wire.
func (e *Engine) Push(ctx context.Context, opts SyncOptions) (PushResult, error) {
	cl, cfg, err := e.newClient(ctx)
	if err != nil {
		return PushResult{}, err
	}
	progress := opts.progress()

	// Read phase: no lock, no network mutation.
	local, err := e.localGraph(ctx)
	if err != nil {
		return PushResult{}, err
	}
	base, err := e.baseGraph(ctx)
	if err != nil {
		return PushResult{}, err
	}
	srv, err := cl.Graph(ctx)
	if err != nil {
		return PushResult{}, fmt.Errorf("reading server graph: %w", err)
	}

	casBase := cfg.SyncedGen
	if srv.Generation != cfg.SyncedGen {
		if !opts.Force {
			return PushResult{}, fmt.Errorf(
				"the server has changes this machine has not seen "+
					"(server at generation %d, last synced %d); run 'spor pull' first, "+
					"or 'spor push --force' to overwrite the server",
				srv.Generation, cfg.SyncedGen)
		}
		casBase = srv.Generation
	}
	if srv.Generation == cfg.SyncedGen && graphsEqual(local, base) {
		return PushResult{NoOp: true, Generation: srv.Generation, States: len(local)}, nil
	}

	// Work out every blob the pushed graph depends on: each state's manifest, and
	// every content blob those manifests name.
	states := sortedStates(local)
	manifests := make(map[string][]byte, len(states))
	content := make(map[string]struct{})
	for i, s := range states {
		entries, raw, err := e.manifestBytes(ctx, s.ID, s.ManifestHash)
		if err != nil {
			return PushResult{}, err
		}
		manifests[s.ManifestHash] = raw
		for _, ent := range entries {
			content[ent.hash] = struct{}{}
		}
		progress(SyncScan, i+1, len(states))
	}

	all := make([]string, 0, len(content)+len(manifests))
	for h := range content {
		all = append(all, h)
	}
	for h := range manifests {
		all = append(all, h)
	}
	sort.Strings(all)

	missing, err := missingBlobs(ctx, cl, all)
	if err != nil {
		return PushResult{}, fmt.Errorf("asking the server which blobs it needs: %w", err)
	}

	res := PushResult{States: len(states)}

	// Content blobs first, then the manifests that name them, then the graph that
	// names the manifests. Each layer is complete before anything points at it, so
	// an interrupted push leaves the server with unreferenced blobs, never a
	// dangling reference.
	upload := make([]string, 0, len(missing))
	for h := range missing {
		if _, isManifest := manifests[h]; !isManifest {
			upload = append(upload, h)
		}
	}
	sort.Strings(upload)

	for i, h := range upload {
		rc, err := e.blobs.Open(h)
		if err != nil {
			return res, fmt.Errorf("reading local blob %s: %w", h, err)
		}
		cr := &countingReader{r: rc}
		err = cl.PutBlob(ctx, h, cr)
		rc.Close()
		if err != nil {
			return res, fmt.Errorf("uploading blob %s: %w", h, err)
		}
		res.BlobsUploaded++
		res.BytesUploaded += cr.n
		progress(SyncUpload, i+1, len(missing))
	}

	manifestHashes := make([]string, 0, len(manifests))
	for h := range manifests {
		if _, needed := missing[h]; needed {
			manifestHashes = append(manifestHashes, h)
		}
	}
	sort.Strings(manifestHashes)
	for i, h := range manifestHashes {
		if err := cl.PutBlob(ctx, h, strings.NewReader(string(manifests[h]))); err != nil {
			return res, fmt.Errorf("uploading manifest %s: %w", h, err)
		}
		res.BlobsUploaded++
		res.BytesUploaded += int64(len(manifests[h]))
		progress(SyncUpload, len(upload)+i+1, len(missing))
	}

	gotGen, err := cl.PushGraph(ctx, casBase, states)
	if err != nil {
		var conflict *remote.ConflictError
		if errors.As(err, &conflict) {
			return res, fmt.Errorf(
				"the server changed while this push was in flight (now at generation %d); "+
					"run 'spor pull' and push again", conflict.Generation)
		}
		return res, fmt.Errorf("pushing the state graph: %w", err)
	}
	res.Generation = gotGen

	// Commit phase: the lock is held only for these few writes, never across the
	// network. The base records what was actually sent; if the local graph moved
	// while the upload ran, the next push carries the difference.
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return res, err
	}
	defer func() { _ = wl.Release() }()

	err = e.inTx(ctx, func(q *gen.Queries) error {
		if err := writeBase(ctx, q, local); err != nil {
			return err
		}
		return q.SetSyncedGen(ctx, gen.SetSyncedGenParams{
			SyncedGen: gotGen,
			SyncedAt:  sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true},
		})
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// Pull brings the server's history into this machine.
//
// It is a three-way merge against the last-synced graph, so a thin on the other
// machine removes states here too, rather than being undone. Work only this
// machine has is kept: two machines that both snapped simply produce a branch.
// Nothing is deleted that local HEAD or a local state depends on.
func (e *Engine) Pull(ctx context.Context, opts SyncOptions) (PullResult, error) {
	cl, cfg, err := e.newClient(ctx)
	if err != nil {
		return PullResult{}, err
	}
	progress := opts.progress()

	srvGraph, err := cl.Graph(ctx)
	if err != nil {
		return PullResult{}, fmt.Errorf("reading server graph: %w", err)
	}
	srv := srvGraph.Index()

	base, err := e.baseGraph(ctx)
	if err != nil {
		return PullResult{}, err
	}
	local, err := e.localGraph(ctx)
	if err != nil {
		return PullResult{}, err
	}
	pins, err := e.headPins(ctx, local)
	if err != nil {
		return PullResult{}, err
	}

	merged, err := mergeGraphs(base, local, srv, pins, opts.Force)
	if err != nil {
		return PullResult{}, err
	}
	if len(merged.Conflicts) > 0 {
		return PullResult{}, &SyncConflictError{Conflicts: merged.Conflicts}
	}
	if srvGraph.Generation == cfg.SyncedGen && graphsEqual(merged.States, local) {
		return PullResult{NoOp: true, Generation: srvGraph.Generation}, nil
	}

	// Fetch every manifest and blob the merged graph needs and this machine lacks.
	manifests, downloaded, err := e.fetchMissing(ctx, cl, merged.States, local, progress)
	if err != nil {
		return PullResult{}, err
	}

	// Apply phase. The graph is re-merged against a freshly read local graph,
	// because a snapshot may have landed while the download ran and the earlier
	// merge would delete states it never saw. The server's graph is fixed, so this
	// cannot require anything that was not already downloaded.
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return PullResult{}, err
	}
	defer func() { _ = wl.Release() }()

	local, err = e.localGraph(ctx)
	if err != nil {
		return PullResult{}, err
	}
	pins, err = e.headPins(ctx, local)
	if err != nil {
		return PullResult{}, err
	}
	merged, err = mergeGraphs(base, local, srv, pins, opts.Force)
	if err != nil {
		return PullResult{}, err
	}
	if len(merged.Conflicts) > 0 {
		return PullResult{}, &SyncConflictError{Conflicts: merged.Conflicts}
	}

	res := PullResult{
		BlobsDownloaded: downloaded,
		Resurrected:     merged.Resurrected,
		ForcedRemote:    merged.ForcedRemote,
		LabelsCleared:   merged.LabelsClears,
		Generation:      srvGraph.Generation,
	}
	if err := e.applyMerge(ctx, merged, local, manifests, srv, srvGraph.Generation, &res, progress); err != nil {
		return PullResult{}, err
	}
	return res, nil
}

// fetchMissing downloads the manifest of every state the merge adds, plus any
// content blob those manifests name that the local store lacks. It returns the
// parsed manifests keyed by state id.
func (e *Engine) fetchMissing(
	ctx context.Context,
	cl *remote.Client,
	merged, local map[string]remote.State,
	progress func(SyncPhase, int, int),
) (map[string][]manifestEntry, int, error) {
	var wanted []remote.State
	for id, s := range merged {
		if _, have := local[id]; !have {
			wanted = append(wanted, s)
		}
	}
	sort.Slice(wanted, func(i, j int) bool { return wanted[i].ID < wanted[j].ID })

	manifests := make(map[string][]manifestEntry, len(wanted))
	content := make(map[string]struct{})

	for i, s := range wanted {
		raw, err := e.downloadBlob(ctx, cl, s.ManifestHash)
		if err != nil {
			return nil, 0, fmt.Errorf("downloading manifest of %s: %w", s.ID, err)
		}
		entries, err := parseManifest(raw)
		if err != nil {
			return nil, 0, fmt.Errorf("parsing manifest of %s: %w", s.ID, err)
		}
		manifests[s.ID] = entries
		for _, ent := range entries {
			if !e.blobs.Has(ent.hash) {
				content[ent.hash] = struct{}{}
			}
		}
		progress(SyncScan, i+1, len(wanted))
	}

	hashes := make([]string, 0, len(content))
	for h := range content {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)

	downloaded := len(wanted) // the manifests themselves
	for i, h := range hashes {
		raw, err := e.downloadBlob(ctx, cl, h)
		if err != nil {
			return nil, 0, fmt.Errorf("downloading blob %s: %w", h, err)
		}
		got, err := e.blobs.Put(strings.NewReader(string(raw)))
		if err != nil {
			return nil, 0, fmt.Errorf("storing blob %s: %w", h, err)
		}
		if got != h {
			return nil, 0, fmt.Errorf("blob %s stored as %s: content does not match its name", h, got)
		}
		downloaded++
		progress(SyncDownload, i+1, len(hashes))
	}
	return manifests, downloaded, nil
}

// downloadBlob fetches a blob and checks it against the hash it was asked for.
// Content addressing is only worth anything if the name is verified.
func (e *Engine) downloadBlob(ctx context.Context, cl *remote.Client, hash string) ([]byte, error) {
	rc, err := cl.GetBlob(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if got := hashBytes(raw); got != hash {
		return nil, fmt.Errorf("server returned content hashing to %s, not %s", got, hash)
	}
	return raw, nil
}

// applyMerge writes the merged graph in one transaction. Callers hold the write
// lock.
//
// The order matters throughout. Labels are cleared first, because the UNIQUE
// index would reject a name moving from one state to another mid-transaction.
// Inserts run parents-before-children and deletes children-before-parents, both
// because parent_id is ON DELETE RESTRICT with foreign keys on. Re-parenting
// happens between the two, so a state about to be deleted no longer has children
// pointing at it.
func (e *Engine) applyMerge(
	ctx context.Context,
	merged mergeResult,
	local map[string]remote.State,
	manifests map[string][]manifestEntry,
	srv map[string]remote.State,
	generation int64,
	res *PullResult,
	progress func(SyncPhase, int, int),
) error {
	ordered, err := insertOrder(merged.States)
	if err != nil {
		return err
	}

	var adds []remote.State
	for _, s := range ordered {
		if _, have := local[s.ID]; !have {
			if _, ok := manifests[s.ID]; !ok {
				return fmt.Errorf(
					"state %s appeared locally while the pull was running; run 'spor pull' again", s.ID)
			}
			adds = append(adds, s)
		}
	}

	del := make(map[string]struct{})
	for id := range local {
		if _, keep := merged.States[id]; !keep {
			del[id] = struct{}{}
		}
	}
	dels := deleteOrder(local, del)

	res.Added = len(adds)
	res.Removed = len(dels)

	total := len(adds) + len(dels) + len(local)
	done := 0

	return e.inTx(ctx, func(q *gen.Queries) error {
		// Clear every label that is moving or going away, before anything tries to
		// claim it.
		for id, s := range local {
			if s.Label == "" {
				continue
			}
			m, kept := merged.States[id]
			if kept && m.Label == s.Label {
				continue
			}
			if err := q.SetStateLabel(ctx, gen.SetStateLabelParams{ID: id}); err != nil {
				return fmt.Errorf("clearing label of %s: %w", id, err)
			}
		}

		for _, s := range adds {
			if err := q.CreateState(ctx, gen.CreateStateParams{
				ID:           s.ID,
				CreatedAt:    s.CreatedAt,
				ParentID:     nullString(s.Parent),
				ManifestHash: s.ManifestHash,
				Label:        nullString(s.Label),
			}); err != nil {
				return fmt.Errorf("creating state %s: %w", s.ID, err)
			}
			for _, ent := range manifests[s.ID] {
				if err := q.AddManifestEntry(ctx, gen.AddManifestEntryParams{
					StateID:    s.ID,
					Path:       ent.path,
					BlobHash:   ent.hash,
					Executable: boolToInt(ent.exec),
				}); err != nil {
					return fmt.Errorf("adding manifest entry %s of %s: %w", ent.path, s.ID, err)
				}
			}
			done++
			progress(SyncApply, done, total)
		}

		// Re-parent and relabel the states both sides already had.
		for id, m := range merged.States {
			l, existed := local[id]
			if !existed {
				continue
			}
			if m.Parent != l.Parent {
				if m.Parent == "" {
					if err := q.SetStateParentNull(ctx, id); err != nil {
						return fmt.Errorf("detaching state %s: %w", id, err)
					}
				} else if err := q.SetStateParent(ctx, gen.SetStateParentParams{
					ParentID: nullString(m.Parent),
					ID:       id,
				}); err != nil {
					return fmt.Errorf("re-parenting state %s: %w", id, err)
				}
				res.Updated++
			}
			if m.Label != l.Label {
				if m.Label != "" {
					if err := q.SetStateLabel(ctx, gen.SetStateLabelParams{
						Label: nullString(m.Label),
						ID:    id,
					}); err != nil {
						return fmt.Errorf("labeling state %s: %w", id, err)
					}
				}
				res.Updated++
			}
			done++
			progress(SyncApply, done, total)
		}

		for _, id := range dels {
			if err := q.DeleteState(ctx, id); err != nil {
				return fmt.Errorf("deleting state %s: %w", id, err)
			}
			done++
			progress(SyncApply, done, total)
		}

		// The base is what the server has, not what this machine now has: the next
		// push must still see the local additions this pull kept.
		if err := writeBase(ctx, q, srv); err != nil {
			return err
		}
		return q.SetSyncedGen(ctx, gen.SetSyncedGenParams{
			SyncedGen: generation,
			SyncedAt:  sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true},
		})
	})
}
