package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/emprcl/spor/internal/remote"
	"github.com/emprcl/spor/internal/remote/remotetest"
)

const syncTestProject = "01JSYNCTESTPROJECT000000000"

// isolateConfig points the credentials file at a temp directory and clears any
// ambient token, so tests never read or write the developer's real config.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("HOME", dir)            // macOS, and a backstop elsewhere
	t.Setenv(TokenEnvVar, "")
}

// syncPair sets up two machines sharing one project on one fake server, the
// arrangement every sync scenario needs.
func syncPair(t *testing.T) (a, b *Engine, rootA, rootB string, srv *remotetest.Server) {
	t.Helper()
	isolateConfig(t)
	srv = remotetest.New(t, "")

	a, rootA = newTestEngine(t)
	b, rootB = newTestEngine(t)
	for _, e := range []*Engine{a, b} {
		e.httpClient = srv.Client()
		if _, err := e.RemoteAdd(context.Background(), srv.URL, syncTestProject, ""); err != nil {
			t.Fatalf("RemoteAdd: %v", err)
		}
	}
	return a, b, rootA, rootB, srv
}

// graphOf reads an engine's state graph for comparison.
func graphOf(t *testing.T, eng *Engine) map[string]remote.State {
	t.Helper()
	g, err := eng.localGraph(context.Background())
	if err != nil {
		t.Fatalf("localGraph: %v", err)
	}
	return g
}

// assertSameGraph fails unless both machines hold identical history.
func assertSameGraph(t *testing.T, a, b *Engine) {
	t.Helper()
	ga, gb := graphOf(t, a), graphOf(t, b)
	if graphsEqual(ga, gb) {
		return
	}
	t.Errorf("graphs differ:\n  a: %s\n  b: %s", describeGraph(ga), describeGraph(gb))
}

func describeGraph(g map[string]remote.State) string {
	ids := make([]string, 0, len(g))
	for id := range g {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		s := g[id]
		parts = append(parts, fmt.Sprintf("%s(parent=%s,label=%s)", short(id), short(s.Parent), s.Label))
	}
	return strings.Join(parts, " ")
}

func short(id string) string {
	if len(id) > 6 {
		return id[:6]
	}
	return id
}

func mustPush(t *testing.T, eng *Engine) PushResult {
	t.Helper()
	res, err := eng.Push(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	return res
}

func mustPull(t *testing.T, eng *Engine) PullResult {
	t.Helper()
	res, err := eng.Pull(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	return res
}

// snapWith writes a file and snapshots, the unit of work in these scenarios.
func snapWith(t *testing.T, eng *Engine, root, name, content string) string {
	t.Helper()
	write(t, root, name, content)
	return snap(t, eng)
}

// TestPushThenPullReplicatesHistory is the backup-and-second-machine base case.
func TestPushThenPullReplicatesHistory(t *testing.T) {
	a, b, rootA, _, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	tip := snapWith(t, a, rootA, "f.txt", "two")
	mustPush(t, a)

	res := mustPull(t, b)
	if res.Added != 2 {
		t.Errorf("pull added %d states, want 2", res.Added)
	}
	assertSameGraph(t, a, b)
	mustVerifyClean(t, b)

	// The manifests must have travelled too, not just the state rows.
	files, err := b.Files(context.Background(), tip)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) == 0 {
		t.Error("pulled state records no files")
	}
}

// TestPullLeavesHeadUnset documents a deliberate consequence of HEAD being
// per-machine (docs/design-spec.md §7): a freshly pulled store has no current
// state, because setting one without materializing the working tree would make @
// disagree with what is on disk.
func TestPullLeavesHeadUnset(t *testing.T) {
	a, b, rootA, _, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	mustPush(t, a)
	mustPull(t, b)

	if got := headID(t, b); got != "" {
		t.Errorf("HEAD = %q after a first pull, want unset", got)
	}
}

// TestPulledContentMatchesOriginal checks the bytes survive the round trip, which
// the manifest-as-blob scheme is entirely responsible for.
func TestPulledContentMatchesOriginal(t *testing.T) {
	a, b, rootA, rootB, _ := syncPair(t)

	state := snapWith(t, a, rootA, "f.txt", "the original content")
	mustPush(t, a)
	mustPull(t, b)

	// HEAD is per-machine and never synced (docs/design-spec.md §7), so a freshly
	// pulled store has none: the state has to be named explicitly.
	if _, err := b.Go(context.Background(), state); err != nil {
		t.Fatalf("Go: %v", err)
	}
	if got := readWorking(t, rootB); got != "the original content" {
		t.Errorf("pulled content = %q, want %q", got, "the original content")
	}
}

// TestThinPropagates is the case the additive-only design in §7 could not serve:
// space reclaimed on one machine must actually be reclaimed on the other, rather
// than being re-hydrated by the next pull.
func TestThinPropagates(t *testing.T) {
	a, b, rootA, _, _ := syncPair(t)

	for _, c := range []string{"one", "two", "three", "four"} {
		snapWith(t, a, rootA, "f.txt", c)
	}
	mustPush(t, a)
	mustPull(t, b)

	before := countStates(t, b)

	if _, err := a.Thin(context.Background()); err != nil {
		t.Fatalf("Thin: %v", err)
	}
	thinned := countStates(t, a)
	if thinned >= before {
		t.Fatalf("thin dropped nothing: %d states before, %d after", before, thinned)
	}
	mustPush(t, a)

	res := mustPull(t, b)
	if res.Removed == 0 {
		t.Error("pull removed no states, so the thin did not propagate")
	}
	if got := countStates(t, b); got != thinned {
		t.Errorf("b has %d states after pulling a thin, want %d", got, thinned)
	}
	assertSameGraph(t, a, b)
	mustVerifyClean(t, b)
}

// TestOfflineAdditionsOnBothMachinesBecomeABranch: the ordinary divergence case
// for one person with two machines. Nothing is lost and nothing is asked of them.
func TestOfflineAdditionsOnBothMachinesBecomeABranch(t *testing.T) {
	a, b, rootA, rootB, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "shared")
	mustPush(t, a)
	mustPull(t, b)

	aOnly := snapWith(t, a, rootA, "f.txt", "from a")
	bOnly := snapWith(t, b, rootB, "f.txt", "from b")

	mustPush(t, a)
	res := mustPull(t, b)
	if res.Added != 1 {
		t.Errorf("pull added %d states, want a's single new state", res.Added)
	}

	g := graphOf(t, b)
	if _, ok := g[aOnly]; !ok {
		t.Error("a's state is missing after the pull")
	}
	if _, ok := g[bOnly]; !ok {
		t.Error("b's own state was lost by the pull")
	}
	mustVerifyClean(t, b)

	// b can now push the union back, and a converges on it.
	mustPush(t, b)
	mustPull(t, a)
	assertSameGraph(t, a, b)
}

// TestPushRefusesWhenServerMoved is the compare-and-swap: b must not clobber a's
// work just because it pushed second.
func TestPushRefusesWhenServerMoved(t *testing.T) {
	a, b, rootA, rootB, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "shared")
	mustPush(t, a)
	mustPull(t, b)

	snapWith(t, a, rootA, "f.txt", "from a")
	mustPush(t, a)

	snapWith(t, b, rootB, "f.txt", "from b")
	_, err := b.Push(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("pushing over another machine's work must be refused")
	}
	if !strings.Contains(err.Error(), "pull") {
		t.Errorf("error should point at pull, got: %v", err)
	}
}

// TestPushForceOverwritesServer is the documented escape hatch.
func TestPushForceOverwritesServer(t *testing.T) {
	a, b, rootA, rootB, srv := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "shared")
	mustPush(t, a)
	mustPull(t, b)

	snapWith(t, a, rootA, "f.txt", "from a")
	mustPush(t, a)

	snapWith(t, b, rootB, "f.txt", "from b")
	if _, err := b.Push(context.Background(), SyncOptions{Force: true}); err != nil {
		t.Fatalf("forced push: %v", err)
	}

	local := graphOf(t, b)
	if got := len(srv.Graph(syncTestProject)); got != len(local) {
		t.Errorf("server holds %d states, want b's %d", got, len(local))
	}
}

// TestDeleteVersusEditRefusesThenForceResolves covers the genuine ambiguity: one
// machine destroyed a state the other was still editing.
func TestDeleteVersusEditRefusesThenForceResolves(t *testing.T) {
	a, b, rootA, _, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	mid := snapWith(t, a, rootA, "f.txt", "two")
	snapWith(t, a, rootA, "f.txt", "three")
	mustPush(t, a)
	mustPull(t, b)

	// a destroys the middle state; b names it.
	if _, err := a.Drop(context.Background(), mid); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	mustPush(t, a)
	if _, err := b.Label(context.Background(), mid, "keeper"); err != nil {
		t.Fatalf("Label: %v", err)
	}

	_, err := b.Pull(context.Background(), SyncOptions{})
	var conflict *SyncConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("pull error = %v, want *SyncConflictError", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("conflict message should name the way out, got: %v", err)
	}

	// Forcing takes the server's history.
	res, err := b.Pull(context.Background(), SyncOptions{Force: true})
	if err != nil {
		t.Fatalf("forced pull: %v", err)
	}
	if len(res.ForcedRemote) == 0 {
		t.Error("forced pull should report what it overrode")
	}
	mustVerifyClean(t, b)
	assertSameGraph(t, a, b)
}

// TestPullNeverDeletesLocalHead guards the foreign keys behind HEAD: head is
// ON DELETE SET NULL and head_history is ON DELETE CASCADE, so a remote thin
// deleting local HEAD would null it and prune the journal silently.
func TestPullNeverDeletesLocalHead(t *testing.T) {
	a, b, rootA, _, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	mid := snapWith(t, a, rootA, "f.txt", "two")
	snapWith(t, a, rootA, "f.txt", "three")
	mustPush(t, a)
	mustPull(t, b)

	// b parks HEAD on the state a is about to destroy.
	if _, err := b.Go(context.Background(), mid); err != nil {
		t.Fatalf("Go: %v", err)
	}
	if headID(t, b) != mid {
		t.Fatalf("HEAD did not move to %s", mid)
	}

	if _, err := a.Drop(context.Background(), mid); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	mustPush(t, a)
	mustPull(t, b)

	if headID(t, b) != mid {
		t.Errorf("HEAD = %q after pull, want it pinned at %s", headID(t, b), mid)
	}
	if _, ok := graphOf(t, b)[mid]; !ok {
		t.Error("the state HEAD points at was deleted by the pull")
	}
	mustVerifyClean(t, b)
}

// TestPullResurrectsAncestorOfLocalWork: a must not be able to delete ground that
// b has since built on.
func TestPullResurrectsAncestorOfLocalWork(t *testing.T) {
	a, b, rootA, rootB, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	mid := snapWith(t, a, rootA, "f.txt", "two")
	snapWith(t, a, rootA, "f.txt", "three")
	mustPush(t, a)
	mustPull(t, b)

	// b builds on the middle state, a destroys it.
	if _, err := b.Go(context.Background(), mid); err != nil {
		t.Fatalf("Go: %v", err)
	}
	child := snapWith(t, b, rootB, "f.txt", "b's work")

	if _, err := a.Drop(context.Background(), mid); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	mustPush(t, a)

	res := mustPull(t, b)
	g := graphOf(t, b)
	if _, ok := g[child]; !ok {
		t.Fatal("b's own work was deleted")
	}
	if _, ok := g[mid]; !ok {
		t.Error("the ancestor b's work hangs off was deleted")
	}
	if len(res.Resurrected) == 0 {
		t.Error("the resurrection should be reported")
	}
	mustVerifyClean(t, b)
}

// TestBlobsAreUploadedBeforeTheGraph is the on-the-wire form of the local
// blobs-before-states invariant: an interrupted push must never leave the server
// with a state referencing content it does not have.
func TestBlobsAreUploadedBeforeTheGraph(t *testing.T) {
	a, _, rootA, _, srv := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	snapWith(t, a, rootA, "f.txt", "two")
	srv.ResetOps()
	mustPush(t, a)

	ops := srv.Ops()
	graphAt := -1
	for i, op := range ops {
		if strings.HasPrefix(op, "put-graph") {
			graphAt = i
		}
	}
	if graphAt < 0 {
		t.Fatalf("no graph push recorded in %v", ops)
	}
	for i, op := range ops {
		if strings.HasPrefix(op, "put-blob") && i > graphAt {
			t.Errorf("blob uploaded after the graph swap: %v", ops)
		}
	}
}

// TestInterruptedPushResumes: a failed upload leaves harmless orphan blobs on the
// server, and the next push completes without re-sending what already arrived.
func TestInterruptedPushResumes(t *testing.T) {
	a, _, rootA, _, srv := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	snapWith(t, a, rootA, "g.txt", "two")

	failed := 0
	srv.FailBlobPut(func(string) error {
		failed++
		if failed > 1 {
			return errors.New("simulated network failure")
		}
		return nil
	})
	if _, err := a.Push(context.Background(), SyncOptions{}); err == nil {
		t.Fatal("push should fail while uploads are failing")
	}
	if srv.Generation(syncTestProject) != 0 {
		t.Error("the graph must not be swapped when blobs failed to upload")
	}

	srv.FailBlobPut(nil)
	res := mustPush(t, a)
	if res.Generation != 1 {
		t.Errorf("generation = %d after recovery, want 1", res.Generation)
	}
	if !graphsEqual(graphOf(t, a), remote.Graph{States: srv.Graph(syncTestProject)}.Index()) {
		t.Error("server graph does not match local after the retried push")
	}
}

// TestPushAndPullAreNoOpsWhenNothingChanged keeps repeated syncs cheap and quiet.
func TestPushAndPullAreNoOpsWhenNothingChanged(t *testing.T) {
	a, _, rootA, _, srv := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	mustPush(t, a)

	if res := mustPush(t, a); !res.NoOp {
		t.Errorf("second push should be a no-op, got %+v", res)
	}
	if res := mustPull(t, a); !res.NoOp {
		t.Errorf("pull after push should be a no-op, got %+v", res)
	}
	if got := srv.Generation(syncTestProject); got != 1 {
		t.Errorf("no-op sync bumped the generation to %d", got)
	}
}

// TestLabelCollisionResolvesTowardTheServer: label carries a UNIQUE index, so a
// name given to different states on each machine has to give way somewhere. A
// cleared label is cheap; a blocked pull is not.
func TestLabelCollisionResolvesTowardTheServer(t *testing.T) {
	a, b, rootA, rootB, _ := syncPair(t)

	snapWith(t, a, rootA, "f.txt", "one")
	mustPush(t, a)
	mustPull(t, b)

	aState := snapWith(t, a, rootA, "f.txt", "from a")
	if _, err := a.Label(context.Background(), aState, "v2"); err != nil {
		t.Fatalf("Label on a: %v", err)
	}
	mustPush(t, a)

	bState := snapWith(t, b, rootB, "f.txt", "from b")
	if _, err := b.Label(context.Background(), bState, "v2"); err != nil {
		t.Fatalf("Label on b: %v", err)
	}

	res := mustPull(t, b)
	if len(res.LabelsCleared) != 1 {
		t.Fatalf("want one reported label clear, got %+v", res.LabelsCleared)
	}
	if c := res.LabelsCleared[0]; c.Label != "v2" || c.Kept != aState || c.Cleared != bState {
		t.Errorf("labelClear = %+v, want v2 kept on a's state", c)
	}
	mustVerifyClean(t, b)
}

// TestPullWithoutRemoteIsClear: the error should say what to do.
func TestPullWithoutRemoteIsClear(t *testing.T) {
	isolateConfig(t)
	eng, _ := newTestEngine(t)

	if _, err := eng.Pull(context.Background(), SyncOptions{}); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("error = %v, want ErrNoRemote", err)
	}
}

// TestRemoteAddAndForget covers the configuration round trip.
func TestRemoteAddAndForget(t *testing.T) {
	isolateConfig(t)
	srv := remotetest.New(t, "")
	eng, _ := newTestEngine(t)
	eng.httpClient = srv.Client()
	ctx := context.Background()

	info, err := eng.RemoteAdd(ctx, srv.URL, "", "")
	if err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	if info.URL != srv.URL {
		t.Errorf("URL = %q, want %q", info.URL, srv.URL)
	}
	if info.ProjectID == "" {
		t.Error("a project id should have been minted")
	}

	// Re-adding the same remote keeps the project id, so sync state is not lost.
	again, err := eng.RemoteAdd(ctx, srv.URL, "", "")
	if err != nil {
		t.Fatalf("RemoteAdd again: %v", err)
	}
	if again.ProjectID != info.ProjectID {
		t.Errorf("project id changed on re-add: %q then %q", info.ProjectID, again.ProjectID)
	}

	if err := eng.RemoteForget(ctx); err != nil {
		t.Fatalf("RemoteForget: %v", err)
	}
	if _, err := eng.Remote(ctx); !errors.Is(err, ErrNoRemote) {
		t.Errorf("after forget, Remote() = %v, want ErrNoRemote", err)
	}
}

// TestRemoteAddResetsBaseWhenRemoteChanges: the sync base describes a
// relationship with one server and is meaningless against another.
func TestRemoteAddResetsBaseWhenRemoteChanges(t *testing.T) {
	a, _, rootA, _, _ := syncPair(t)
	ctx := context.Background()

	snapWith(t, a, rootA, "f.txt", "one")
	mustPush(t, a)

	base, err := a.baseGraph(ctx)
	if err != nil {
		t.Fatalf("baseGraph: %v", err)
	}
	if len(base) == 0 {
		t.Fatal("push should have recorded a sync base")
	}

	other := remotetest.New(t, "")
	if _, err := a.RemoteAdd(ctx, other.URL, syncTestProject, ""); err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	base, err = a.baseGraph(ctx)
	if err != nil {
		t.Fatalf("baseGraph: %v", err)
	}
	if len(base) != 0 {
		t.Errorf("sync base survived a remote change: %d entries", len(base))
	}
	info, err := a.Remote(ctx)
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	if info.SyncedGen != 0 {
		t.Errorf("synced generation = %d after remote change, want 0", info.SyncedGen)
	}
}

// TestAuthTokenRoundTrip stores a token and uses it against a server that
// requires one.
func TestAuthTokenRoundTrip(t *testing.T) {
	isolateConfig(t)
	srv := remotetest.New(t, "sekrit")
	eng, root := newTestEngine(t)
	eng.httpClient = srv.Client()
	ctx := context.Background()

	if _, err := eng.RemoteAdd(ctx, srv.URL, syncTestProject, "sekrit"); err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	info, err := eng.Remote(ctx)
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	if !info.HasToken {
		t.Error("stored token not reported")
	}

	snapWith(t, eng, root, "f.txt", "one")
	if _, err := eng.Push(ctx, SyncOptions{}); err != nil {
		t.Fatalf("push with stored token: %v", err)
	}
}

// TestTokenEnvVarOverridesStoredToken matches the $SPOR_PAGER convention and
// gives CI somewhere to put a token without writing a file.
func TestTokenEnvVarOverridesStoredToken(t *testing.T) {
	isolateConfig(t)
	srv := remotetest.New(t, "from-env")
	eng, root := newTestEngine(t)
	eng.httpClient = srv.Client()
	ctx := context.Background()

	if _, err := eng.RemoteAdd(ctx, srv.URL, syncTestProject, "stored-and-wrong"); err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	t.Setenv(TokenEnvVar, "from-env")

	snapWith(t, eng, root, "f.txt", "one")
	if _, err := eng.Push(ctx, SyncOptions{}); err != nil {
		t.Fatalf("push with env token: %v", err)
	}
}

// TestSerializedManifestMatchesHash is the invariant the whole manifest-as-blob
// scheme rests on: the bytes sent over the wire are exactly the bytes spor
// hashes, so the blob's content hash is the state's manifest hash.
func TestSerializedManifestMatchesHash(t *testing.T) {
	entries := []manifestEntry{
		{path: "a.txt", hash: "aaa", exec: false},
		{path: "dir/b.sh", hash: "bbb", exec: true},
		{path: "odd\nname.txt", hash: "ccc", exec: false},
	}

	raw := serializeManifest(entries)
	sum := sha256.Sum256(raw)
	if got, want := hex.EncodeToString(sum[:]), hashManifest(entries); got != want {
		t.Errorf("sha256(serializeManifest) = %s, hashManifest = %s", got, want)
	}
}

// TestManifestRoundTrip covers parsing, including a path containing a newline,
// which is why the format is parsed field by field rather than split on lines.
func TestManifestRoundTrip(t *testing.T) {
	entries := []manifestEntry{
		{path: "a.txt", hash: "aaa", exec: false},
		{path: "dir/b.sh", hash: "bbb", exec: true},
		{path: "odd\nname.txt", hash: "ccc", exec: false},
	}

	got, err := parseManifest(serializeManifest(entries))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for i := range entries {
		if got[i] != entries[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], entries[i])
		}
	}
}

// TestParseManifestRejectsGarbage: a truncated or corrupt manifest must be an
// error, never a silently shorter file list.
func TestParseManifestRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"no separators":  "just some bytes",
		"missing mode":   "a.txt\x00hash\x00",
		"bad mode":       "a.txt\x00hash\x009\n",
		"no newline":     "a.txt\x00hash\x000",
		"truncated hash": "a.txt\x00hash",
	}
	for name, raw := range cases {
		if _, err := parseManifest([]byte(raw)); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

// TestEmptyManifestRoundTrips: a state can legitimately record no files.
func TestEmptyManifestRoundTrips(t *testing.T) {
	got, err := parseManifest(serializeManifest(nil))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want none", len(got))
	}
}
