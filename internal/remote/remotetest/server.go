// Package remotetest provides an in-memory spor server for tests: a
// content-addressed blob store plus one versioned graph document per project,
// which is the whole of what docs/design-spec.md §7 asks a server to be.
//
// It is deliberately stricter than a real server needs to be. It verifies every
// blob against its hash and rejects a graph that references a manifest blob it
// does not hold, so a client that uploads in the wrong order fails loudly here
// rather than corrupting a real store.
package remotetest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/emprcl/spor/internal/remote"
)

// project is one project's server-side state.
type project struct {
	generation int64
	states     []remote.State
}

// Server is a fake spor server. Create it with New; it shuts down via t.Cleanup.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	token    string
	projects map[string]*project
	blobs    map[string][]byte
	ops      []string

	// failBlobPut, when set, is consulted before storing a blob; a non-nil error
	// becomes a 500. It exists to test interrupted uploads.
	failBlobPut func(hash string) error
}

// New starts a fake server. token may be empty to disable auth.
func New(t *testing.T, token string) *Server {
	t.Helper()
	s := &Server{
		token:    token,
		projects: make(map[string]*project),
		blobs:    make(map[string][]byte),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /p/{project}/graph", s.auth(s.getGraph))
	mux.HandleFunc("PUT /p/{project}/graph", s.auth(s.putGraph))
	mux.HandleFunc("POST /p/{project}/blobs/missing", s.auth(s.postMissing))
	mux.HandleFunc("GET /p/{project}/blobs/{hash}", s.auth(s.getBlob))
	mux.HandleFunc("PUT /p/{project}/blobs/{hash}", s.auth(s.putBlob))

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// auth wraps a handler with the single-token bearer check.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// proj returns the named project's state, creating it empty on first use.
// Callers hold s.mu.
func (s *Server) proj(id string) *project {
	p, ok := s.projects[id]
	if !ok {
		p = &project{}
		s.projects[id] = p
	}
	return p
}

func (s *Server) record(format string, args ...any) {
	s.ops = append(s.ops, fmt.Sprintf(format, args...))
}

func (s *Server) getGraph(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.proj(r.PathValue("project"))
	s.record("get-graph")
	writeJSON(w, remote.Graph{Generation: p.generation, States: p.states})
}

func (s *Server) putGraph(w http.ResponseWriter, r *http.Request) {
	var req remote.PushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.proj(r.PathValue("project"))

	// The compare-and-swap: refuse a push based on a generation we have moved past.
	if req.BaseGeneration != p.generation {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, remote.PushResponse{Generation: p.generation})
		s.record("put-graph-conflict")
		return
	}

	// Blobs before the rows that reference them. A real server may be dumber, but
	// a client that gets this wrong should not get away with it in tests.
	for _, st := range req.States {
		if _, ok := s.blobs[st.ManifestHash]; !ok {
			http.Error(w,
				fmt.Sprintf("state %s references manifest blob %s which was never uploaded",
					st.ID, st.ManifestHash),
				http.StatusBadRequest)
			s.record("put-graph-rejected")
			return
		}
	}

	p.states = req.States
	p.generation++
	s.record("put-graph gen=%d states=%d", p.generation, len(p.states))
	writeJSON(w, remote.PushResponse{Generation: p.generation})
}

func (s *Server) postMissing(w http.ResponseWriter, r *http.Request) {
	var req remote.MissingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	missing := []string{}
	for _, h := range req.Hashes {
		if _, ok := s.blobs[h]; !ok {
			missing = append(missing, h)
		}
	}
	s.record("missing asked=%d missing=%d", len(req.Hashes), len(missing))
	writeJSON(w, remote.MissingResponse{Missing: missing})
}

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")

	s.mu.Lock()
	b, ok := s.blobs[hash]
	if ok {
		s.record("get-blob %s", short(hash))
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "no such blob", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(b)
}

func (s *Server) putBlob(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body", http.StatusBadRequest)
		return
	}

	// Content-addressed means exactly this: the name must be the content's hash.
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != hash {
		http.Error(w,
			fmt.Sprintf("blob content hashes to %s, not %s", got, hash),
			http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failBlobPut != nil {
		if err := s.failBlobPut(hash); err != nil {
			s.record("put-blob-failed %s", short(hash))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.blobs[hash] = body
	s.record("put-blob %s", short(hash))
	w.WriteHeader(http.StatusCreated)
}

// Generation reports a project's current generation.
func (s *Server) Generation(projectID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proj(projectID).generation
}

// Graph reports a project's current states.
func (s *Server) Graph(projectID string) []remote.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]remote.State(nil), s.proj(projectID).states...)
}

// BlobCount reports how many distinct blobs the server holds.
func (s *Server) BlobCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.blobs)
}

// HasBlob reports whether the server holds a blob.
func (s *Server) HasBlob(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.blobs[hash]
	return ok
}

// Ops returns the ordered log of handled requests, for asserting on ordering
// such as blobs preceding the graph swap.
func (s *Server) Ops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ops...)
}

// ResetOps clears the operation log, so a test can assert on one phase at a time.
func (s *Server) ResetOps() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops = nil
}

// FailBlobPut installs a hook consulted before each blob is stored; returning an
// error makes that upload fail, which is how an interrupted push is simulated.
// Pass nil to clear.
func (s *Server) FailBlobPut(fn func(hash string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failBlobPut = fn
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func short(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
