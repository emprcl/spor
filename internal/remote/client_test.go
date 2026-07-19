package remote_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/emprcl/spor/internal/remote"
	"github.com/emprcl/spor/internal/remote/remotetest"
)

const testProject = "01JTESTPROJECT0000000000000"

// hashOf returns the content hash a blob is stored under.
func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// newClient starts a fake server and returns a client wired to it.
func newClient(t *testing.T, token string) (*remote.Client, *remotetest.Server) {
	t.Helper()
	srv := remotetest.New(t, token)
	c, err := remote.New(srv.URL, testProject, token, nil)
	if err != nil {
		t.Fatalf("remote.New: %v", err)
	}
	return c, srv
}

// putBlob uploads a blob and returns its hash.
func putBlob(t *testing.T, c *remote.Client, content string) string {
	t.Helper()
	h := hashOf(content)
	if err := c.PutBlob(t.Context(), h, strings.NewReader(content)); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	return h
}

// TestNewRejectsNonHTTPURL keeps a typo'd remote from being stored as if valid.
func TestNewRejectsNonHTTPURL(t *testing.T) {
	for _, raw := range []string{"ftp://example.com", "example.com", ""} {
		if _, err := remote.New(raw, testProject, "", nil); err == nil {
			t.Errorf("remote.New(%q) should fail", raw)
		}
	}
}

// TestGraphOnUnknownProjectIsEmpty: a first push compares against generation 0,
// so an unseen project must read as empty rather than as an error.
func TestGraphOnUnknownProjectIsEmpty(t *testing.T) {
	c, _ := newClient(t, "")

	g, err := c.Graph(t.Context())
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if g.Generation != 0 || len(g.States) != 0 {
		t.Errorf("empty project = gen %d with %d states, want gen 0 empty", g.Generation, len(g.States))
	}
}

// TestPushGraphAdvancesGeneration covers the happy path and the round-trip.
func TestPushGraphAdvancesGeneration(t *testing.T) {
	c, srv := newClient(t, "")
	mh := putBlob(t, c, "manifest bytes")

	states := []remote.State{{ID: "a", CreatedAt: 1, ManifestHash: mh, Label: "start"}}
	gen, err := c.PushGraph(t.Context(), 0, states)
	if err != nil {
		t.Fatalf("PushGraph: %v", err)
	}
	if gen != 1 {
		t.Errorf("generation = %d, want 1", gen)
	}
	if got := srv.Generation(testProject); got != 1 {
		t.Errorf("server generation = %d, want 1", got)
	}

	g, err := c.Graph(t.Context())
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if len(g.States) != 1 || g.States[0].ID != "a" || g.States[0].Label != "start" {
		t.Errorf("round-tripped states = %+v", g.States)
	}
}

// TestPushGraphStaleGenerationConflicts is the guard that keeps one machine from
// overwriting another's work: the second push is based on a generation the
// server has already moved past.
func TestPushGraphStaleGenerationConflicts(t *testing.T) {
	c, _ := newClient(t, "")
	mh := putBlob(t, c, "manifest bytes")
	states := []remote.State{{ID: "a", CreatedAt: 1, ManifestHash: mh}}

	if _, err := c.PushGraph(t.Context(), 0, states); err != nil {
		t.Fatalf("first push: %v", err)
	}

	_, err := c.PushGraph(t.Context(), 0, states) // still claims base 0
	var conflict *remote.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale push error = %v, want *ConflictError", err)
	}
	if conflict.Generation != 1 {
		t.Errorf("conflict reports generation %d, want 1", conflict.Generation)
	}
}

// TestPushGraphRequiresManifestBlobsFirst locks in the ordering invariant: blobs
// before the rows referencing them.
func TestPushGraphRequiresManifestBlobsFirst(t *testing.T) {
	c, _ := newClient(t, "")

	states := []remote.State{{ID: "a", CreatedAt: 1, ManifestHash: hashOf("never uploaded")}}
	if _, err := c.PushGraph(t.Context(), 0, states); err == nil {
		t.Fatal("pushing a graph whose manifest blob is absent must fail")
	}
}

// TestMissingBlobsReportsOnlyAbsentOnes: the batch endpoint is what keeps an
// upload to one round-trip rather than one per blob.
func TestMissingBlobsReportsOnlyAbsentOnes(t *testing.T) {
	c, _ := newClient(t, "")
	present := putBlob(t, c, "here")
	absent := hashOf("not here")

	missing, err := c.MissingBlobs(t.Context(), []string{present, absent})
	if err != nil {
		t.Fatalf("MissingBlobs: %v", err)
	}
	if len(missing) != 1 || missing[0] != absent {
		t.Errorf("missing = %v, want just %v", missing, absent)
	}
}

// TestMissingBlobsEmptyMakesNoRequest avoids a pointless round-trip when there is
// nothing to ask about.
func TestMissingBlobsEmptyMakesNoRequest(t *testing.T) {
	c, srv := newClient(t, "")
	srv.ResetOps()

	missing, err := c.MissingBlobs(t.Context(), nil)
	if err != nil {
		t.Fatalf("MissingBlobs: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
	if ops := srv.Ops(); len(ops) != 0 {
		t.Errorf("no request should have been made, got %v", ops)
	}
}

// TestBlobRoundTrip checks bytes survive intact.
func TestBlobRoundTrip(t *testing.T) {
	c, _ := newClient(t, "")
	const content = "the quick brown fox\x00with a nul"
	h := putBlob(t, c, content)

	rc, err := c.GetBlob(t.Context(), h)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading blob: %v", err)
	}
	if string(got) != content {
		t.Errorf("blob = %q, want %q", got, content)
	}
}

// TestGetMissingBlobIsErrNotFound lets callers distinguish a gap from a failure.
func TestGetMissingBlobIsErrNotFound(t *testing.T) {
	c, _ := newClient(t, "")

	_, err := c.GetBlob(t.Context(), hashOf("absent"))
	if !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// TestPutBlobUnderWrongHashIsRejected: content addressing only means anything if
// the name is checked against the content.
func TestPutBlobUnderWrongHashIsRejected(t *testing.T) {
	c, _ := newClient(t, "")

	err := c.PutBlob(t.Context(), hashOf("one thing"), strings.NewReader("another thing"))
	if err == nil {
		t.Fatal("uploading content under the wrong hash must fail")
	}
}

// TestAuthTokenIsSent and its absence is refused.
func TestAuthTokenIsSent(t *testing.T) {
	_, srv := newClient(t, "sekrit")

	authed, err := remote.New(srv.URL, testProject, "sekrit", nil)
	if err != nil {
		t.Fatalf("remote.New: %v", err)
	}
	if _, err := authed.Graph(t.Context()); err != nil {
		t.Fatalf("correct token should be accepted: %v", err)
	}

	wrong, err := remote.New(srv.URL, testProject, "nope", nil)
	if err != nil {
		t.Fatalf("remote.New: %v", err)
	}
	if _, err := wrong.Graph(t.Context()); err == nil {
		t.Fatal("wrong token must be refused")
	}
}

// TestProjectsAreIsolated: two projects on one server must not see each other's
// graphs, which is the whole point of the project id.
func TestProjectsAreIsolated(t *testing.T) {
	srv := remotetest.New(t, "")
	a, err := remote.New(srv.URL, "project-a", "", nil)
	if err != nil {
		t.Fatalf("remote.New: %v", err)
	}
	b, err := remote.New(srv.URL, "project-b", "", nil)
	if err != nil {
		t.Fatalf("remote.New: %v", err)
	}

	mh := putBlob(t, a, "manifest bytes")
	if _, err := a.PushGraph(t.Context(), 0, []remote.State{{ID: "a1", CreatedAt: 1, ManifestHash: mh}}); err != nil {
		t.Fatalf("push to project a: %v", err)
	}

	g, err := b.Graph(t.Context())
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if g.Generation != 0 || len(g.States) != 0 {
		t.Errorf("project b sees project a's data: %+v", g)
	}
}

// TestGraphIndexKeysByID covers the small helper push and pull both rely on.
func TestGraphIndexKeysByID(t *testing.T) {
	g := remote.Graph{States: []remote.State{{ID: "a"}, {ID: "b"}}}

	idx := g.Index()
	if len(idx) != 2 || idx["a"].ID != "a" || idx["b"].ID != "b" {
		t.Errorf("Index() = %+v", idx)
	}
}
