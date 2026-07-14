package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/emprcl/spor/internal/blob"
	"github.com/emprcl/spor/internal/db/gen"
)

// VerifyIssue is one integrity problem found by Verify. Kind is a stable slug;
// State, Path, and Detail give context for display.
type VerifyIssue struct {
	Kind   string // missing-blob, corrupt-blob, manifest-hash, dangling-parent, dangling-head, cycle
	State  string
	Path   string
	Detail string
}

// VerifyResult is the outcome of an integrity check.
type VerifyResult struct {
	StatesChecked int
	BlobsChecked  int
	Issues        []VerifyIssue
}

// OK reports whether the store passed with no issues.
func (r VerifyResult) OK() bool { return len(r.Issues) == 0 }

// Verify checks store integrity (docs/SPEC.md §8): every referenced blob exists
// and matches its SHA-256; every manifest's stored hash recomputes; every parent
// and HEAD resolves to a real state; and the parent graph is acyclic. It is a
// pure read (referenced blobs are never GC'd, so it needs no lock) and collects
// all problems rather than stopping at the first.
func (e *Engine) Verify(ctx context.Context) (VerifyResult, error) {
	states, err := e.q.ListStates(ctx)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("listing states: %w", err)
	}
	byID := make(map[string]gen.ListStatesRow, len(states))
	for _, s := range states {
		byID[s.ID] = s
	}

	res := VerifyResult{StatesChecked: len(states)}

	head, err := e.q.GetHead(ctx)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	if head.Valid {
		if _, ok := byID[head.String]; !ok {
			res.Issues = append(res.Issues, VerifyIssue{
				Kind: "dangling-head", State: head.String,
				Detail: "HEAD points to a state that does not exist",
			})
		}
	}

	// Per state: parent resolves, manifest hash recomputes, and collect the first
	// place each blob is referenced (for blob-issue reporting).
	refs := make(map[string]blobLoc)
	for _, s := range states {
		if s.ParentID.Valid {
			if _, ok := byID[s.ParentID.String]; !ok {
				res.Issues = append(res.Issues, VerifyIssue{
					Kind: "dangling-parent", State: s.ID,
					Detail: "parent " + shortHash(s.ParentID.String) + " does not exist",
				})
			}
		}
		entries, err := e.q.ListManifestEntries(ctx, s.ID)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("reading manifest of %s: %w", s.ID, err)
		}
		me := make([]manifestEntry, 0, len(entries))
		for _, ent := range entries {
			me = append(me, manifestEntry{path: ent.Path, hash: ent.BlobHash, exec: ent.Executable != 0})
			if _, seen := refs[ent.BlobHash]; !seen {
				refs[ent.BlobHash] = blobLoc{state: s.ID, path: ent.Path}
			}
		}
		stored, err := e.q.GetStateManifestHash(ctx, s.ID)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("reading manifest hash of %s: %w", s.ID, err)
		}
		if hashManifest(me) != stored {
			res.Issues = append(res.Issues, VerifyIssue{
				Kind: "manifest-hash", State: s.ID,
				Detail: "recomputed manifest hash does not match the stored value",
			})
		}
	}

	if at, ok := detectCycle(byID); ok {
		res.Issues = append(res.Issues, VerifyIssue{
			Kind: "cycle", State: at,
			Detail: "the parent graph contains a cycle",
		})
	}

	// Every distinct referenced blob must exist and match its hash.
	res.BlobsChecked = len(refs)
	for hash, ref := range refs {
		ok, err := e.blobs.Verify(hash)
		switch {
		case errors.Is(err, blob.ErrNotFound):
			res.Issues = append(res.Issues, VerifyIssue{
				Kind: "missing-blob", State: ref.state, Path: ref.path,
				Detail: "blob " + shortHash(hash) + " is missing",
			})
		case err != nil || !ok:
			res.Issues = append(res.Issues, VerifyIssue{
				Kind: "corrupt-blob", State: ref.state, Path: ref.path,
				Detail: "blob " + shortHash(hash) + " does not match its hash",
			})
		}
	}
	return res, nil
}

// blobLoc records where a blob is first referenced, so a blob issue can name a
// concrete state and path.
type blobLoc struct{ state, path string }

// detectCycle reports whether the parent graph has a cycle, returning a state on
// it. The graph is functional (one parent per state), so a three-color DFS along
// parent links suffices; a dangling parent is not followed (it is reported
// separately).
func detectCycle(byID map[string]gen.ListStatesRow) (string, bool) {
	const (
		white = iota
		gray
		black
	)
	color := make(map[string]int, len(byID))
	var walk func(id string) (string, bool)
	walk = func(id string) (string, bool) {
		switch color[id] {
		case gray:
			return id, true
		case black:
			return "", false
		}
		color[id] = gray
		if s := byID[id]; s.ParentID.Valid {
			if _, ok := byID[s.ParentID.String]; ok {
				if at, cyc := walk(s.ParentID.String); cyc {
					color[id] = black
					return at, true
				}
			}
		}
		color[id] = black
		return "", false
	}
	for id := range byID {
		if at, cyc := walk(id); cyc {
			return at, true
		}
	}
	return "", false
}

// shortHash abbreviates a hash or id for display.
func shortHash(h string) string {
	const n = 12
	if len(h) > n {
		return h[:n]
	}
	return h
}
