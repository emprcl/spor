// Package remote implements the client half of spor's optional single-user sync
// (docs/design-spec.md §7): the wire types below and the HTTP client in client.go.
// The server is deliberately dumb, a content-addressed blob store plus one small
// versioned document per project.
//
// Two halves of the store travel differently. Blobs hold all the bytes, so they
// stay content-addressed and additive. The state graph is tiny by comparison, so
// it is sent whole and versioned by a generation counter, which is what lets
// deletions and re-parents propagate where a plain id set-difference could not.
package remote

// State is one state row on the wire.
//
// Parent and Label are empty rather than null when absent, so the JSON stays
// free of nulls. ManifestHash names the blob holding the state's canonical
// manifest: a manifest's serialization is exactly the bytes spor already hashes
// to produce that value, so manifests need no endpoint of their own and dedup
// and self-verification come for free.
type State struct {
	ID           string `json:"id"`
	Parent       string `json:"parent,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	ManifestHash string `json:"manifest_hash"`
	Label        string `json:"label,omitempty"`
}

// Graph is a project's whole state graph, the unit of sync. Generation is the
// server's counter: a push carries the generation it was based on, and the server
// accepts the write only if that still matches, so a second machine's work is
// never silently overwritten.
type Graph struct {
	Generation int64   `json:"generation"`
	States     []State `json:"states"`
}

// Index returns the graph's states keyed by id.
func (g Graph) Index() map[string]State {
	m := make(map[string]State, len(g.States))
	for _, s := range g.States {
		m[s.ID] = s
	}
	return m
}

// The request and response bodies below are exported because they are the
// protocol: anything implementing a spor server implements exactly these.

// PushRequest is the body of PUT /graph. BaseGeneration is the compare-and-swap
// precondition: the generation the client believes the server is at.
type PushRequest struct {
	BaseGeneration int64   `json:"base_generation"`
	States         []State `json:"states"`
}

// PushResponse carries the server's generation, both on success and on a 409,
// where it reports the generation the client is behind.
type PushResponse struct {
	Generation int64 `json:"generation"`
}

// MissingRequest asks which of a batch of hashes the server lacks, so an upload
// costs one round-trip rather than one per blob.
type MissingRequest struct {
	Hashes []string `json:"hashes"`
}

// MissingResponse lists the subset of the requested hashes the server needs.
type MissingResponse struct {
	Missing []string `json:"missing"`
}
