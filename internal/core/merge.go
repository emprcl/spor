package core

import (
	"fmt"
	"sort"

	"github.com/emprcl/spor/internal/remote"
)

// This file implements the three-way merge behind pull (docs/design-spec.md §7).
//
// Sync compares three graphs, not two: the last-synced graph (the base, spor's
// equivalent of a remote-tracking branch), the local graph, and the server's.
// The base is what makes deletion expressible. Without it, "state X is absent
// locally" is ambiguous between "I deleted it" and "the server added it", and a
// thin could never propagate.
//
// A state's created_at and manifest_hash never change once written; history
// editing re-parents and relabels but never rewrites contents. So only parent and
// label are ever merged, and disagreement on the immutable fields means a
// corrupt store rather than a conflict.

// SyncConflict is a state both machines edited differently. Conflicts are not
// resolved by default: one user editing history on two machines at once is rare
// enough that guessing is worse than stopping and asking.
type SyncConflict struct {
	StateID string
	Field   string // "parent", "label", or "deleted"
	Local   string
	Remote  string
}

// LabelClear records a label that named two different states after merging.
// Labels carry a UNIQUE index, so one side has to give way.
type LabelClear struct {
	Label   string
	Kept    string // state that keeps the label (the server's)
	Cleared string // state that loses it
}

// mergeResult is the merged graph plus everything worth reporting about how it
// was reached.
type mergeResult struct {
	States       map[string]remote.State
	Resurrected  []string // states a delete would have orphaned
	ForcedRemote []string // conflicts resolved toward the server under --force
	LabelsClears []LabelClear
	Conflicts    []SyncConflict
}

// mergeGraphs merges the local and server graphs against their common base.
//
// pinned lists states that must survive regardless of what the other machine
// did, namely local HEAD and its ancestors: head is ON DELETE SET NULL and
// head_history is ON DELETE CASCADE, so an unpinned delete would quietly null
// HEAD and prune the journal.
//
// preferRemote resolves conflicts in the server's favor instead of reporting
// them, which is what pull --force does. Note that it settles conflicts only; a
// local state the server never knew about is still kept, since discarding work
// that is not actually in dispute would be a much bigger hammer than asked for.
//
// The returned graph is meaningless when Conflicts is non-empty.
func mergeGraphs(
	base, local, srv map[string]remote.State,
	pinned map[string]struct{},
	preferRemote bool,
) (mergeResult, error) {
	res := mergeResult{States: make(map[string]remote.State)}

	ids := make([]string, 0, len(local)+len(srv))
	seen := make(map[string]struct{}, len(local)+len(srv))
	for _, m := range []map[string]remote.State{base, local, srv} {
		for id := range m {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // deterministic conflict ordering

	for _, id := range ids {
		b, inBase := base[id]
		l, inLocal := local[id]
		r, inSrv := srv[id]

		// Absent from both live graphs: deleted on one side and never known to the
		// other, or deleted on both. Either way it goes, subject to pinning below.
		if !inLocal && !inSrv {
			continue
		}

		// Present on one side only.
		if inLocal != inSrv {
			survivor := l
			if inSrv {
				survivor = r
			}
			// The base never had it, so it is that side's new state.
			if !inBase {
				res.States[id] = survivor
				continue
			}
			// The other side deleted it. If the surviving side left it untouched the
			// delete simply propagates; if that side also edited it, the machines
			// disagree about a state one of them destroyed, and dropping it silently
			// would discard the edit.
			if survivor.Parent == b.Parent && survivor.Label == b.Label {
				continue
			}
			if preferRemote {
				// The server's action wins: it deleted, so the state goes; it kept an
				// edited state the local side deleted, so the state stays.
				res.ForcedRemote = append(res.ForcedRemote, id)
				if inSrv {
					res.States[id] = r
				}
				continue
			}
			edited := describeEdit(b, survivor)
			c := SyncConflict{StateID: id, Field: "deleted", Local: edited, Remote: "(deleted)"}
			if inSrv {
				c.Local, c.Remote = "(deleted)", edited
			}
			res.Conflicts = append(res.Conflicts, c)
			continue
		}

		// Present on both. created_at and manifest_hash are immutable, so a
		// disagreement is corruption, not a conflict to merge.
		if l.ManifestHash != r.ManifestHash || l.CreatedAt != r.CreatedAt {
			return mergeResult{}, fmt.Errorf(
				"state %s differs between machines in fields that cannot change "+
					"(manifest %s vs %s); the store may be corrupt, run spor verify",
				id, l.ManifestHash, r.ManifestHash,
			)
		}

		merged := l
		forced := false

		parent, conflicted := mergeField(b.Parent, l.Parent, r.Parent, inBase)
		if conflicted && preferRemote {
			parent, conflicted, forced = r.Parent, false, true
		}
		if conflicted {
			res.Conflicts = append(res.Conflicts, SyncConflict{
				StateID: id, Field: "parent", Local: l.Parent, Remote: r.Parent,
			})
		}
		merged.Parent = parent

		label, conflicted := mergeField(b.Label, l.Label, r.Label, inBase)
		if conflicted && preferRemote {
			label, conflicted, forced = r.Label, false, true
		}
		if conflicted {
			res.Conflicts = append(res.Conflicts, SyncConflict{
				StateID: id, Field: "label", Local: l.Label, Remote: r.Label,
			})
		}
		merged.Label = label

		if forced {
			res.ForcedRemote = append(res.ForcedRemote, id)
		}
		res.States[id] = merged
	}

	if len(res.Conflicts) > 0 {
		return res, nil
	}

	sort.Strings(res.ForcedRemote)
	res.Resurrected = resurrect(res.States, base, local, srv, pinned)
	res.LabelsClears = resolveLabels(res.States, srv)
	return res, nil
}

// describeEdit summarizes how a state was changed from its base, for the message
// attached to a delete/edit conflict.
func describeEdit(base, edited remote.State) string {
	switch {
	case base.Parent != edited.Parent && base.Label != edited.Label:
		return fmt.Sprintf("re-parented and labeled %q", edited.Label)
	case base.Parent != edited.Parent:
		return "re-parented"
	case edited.Label == "":
		return "label cleared"
	default:
		return fmt.Sprintf("labeled %q", edited.Label)
	}
}

// mergeField merges one mutable field. Whichever side changed it wins; if both
// changed it to the same value they agree; if both changed it differently that
// is a conflict. When the base lacks the state there is nothing to have changed
// from, and the two sides must already agree.
func mergeField(base, local, srv string, inBase bool) (string, bool) {
	if local == srv {
		return local, false
	}
	if !inBase {
		return local, true
	}
	switch {
	case local == base:
		return srv, false
	case srv == base:
		return local, false
	default:
		return local, true
	}
}

// resurrect restores states that a delete would have orphaned, and returns their
// ids. A state survives its deletion when it is pinned, or when it is an ancestor
// of a state being kept: the other machine cannot have known about a state this
// one added beneath it, and parent_id is ON DELETE RESTRICT.
//
// It runs to a fixpoint, since resurrecting a state can require resurrecting its
// own parent.
func resurrect(
	merged, base, local, srv map[string]remote.State,
	pinned map[string]struct{},
) []string {
	var restored []string

	revive := func(id string) bool {
		if _, ok := merged[id]; ok {
			return false
		}
		// Prefer the local copy: this machine still holds the state, so its view is
		// the one whose parent link the survivors were built against.
		for _, m := range []map[string]remote.State{local, srv, base} {
			if s, ok := m[id]; ok {
				merged[id] = s
				restored = append(restored, id)
				return true
			}
		}
		return false
	}

	pins := make([]string, 0, len(pinned))
	for id := range pinned {
		pins = append(pins, id)
	}
	sort.Strings(pins)
	for _, id := range pins {
		revive(id)
	}

	for changed := true; changed; {
		changed = false
		ids := make([]string, 0, len(merged))
		for id := range merged {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			parent := merged[id].Parent
			if parent == "" {
				continue
			}
			if revive(parent) {
				changed = true
			}
		}
	}

	// A parent that exists in no graph at all would break the foreign key on
	// insert. Nothing should produce one, so rather than fail the pull, detach the
	// state into a root: the history stays reachable and verify will say so.
	for id, s := range merged {
		if s.Parent == "" {
			continue
		}
		if _, ok := merged[s.Parent]; !ok {
			s.Parent = ""
			merged[id] = s
		}
	}

	sort.Strings(restored)
	return restored
}

// resolveLabels enforces the UNIQUE index on label. When a label ends up naming
// two states, the server's assignment wins and the other state is left unlabeled.
// Refusing the pull instead would be worse: a label is cheap to re-add, and a
// blocked pull is not.
func resolveLabels(merged, srv map[string]remote.State) []LabelClear {
	byLabel := make(map[string][]string)
	for id, s := range merged {
		if s.Label != "" {
			byLabel[s.Label] = append(byLabel[s.Label], id)
		}
	}

	var clears []LabelClear
	labels := make([]string, 0, len(byLabel))
	for l := range byLabel {
		labels = append(labels, l)
	}
	sort.Strings(labels)

	for _, label := range labels {
		ids := byLabel[label]
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)

		keep := ids[0]
		for _, id := range ids {
			if s, ok := srv[id]; ok && s.Label == label {
				keep = id
				break
			}
		}
		for _, id := range ids {
			if id == keep {
				continue
			}
			s := merged[id]
			s.Label = ""
			merged[id] = s
			clears = append(clears, LabelClear{Label: label, Kept: keep, Cleared: id})
		}
	}
	return clears
}

// insertOrder returns the merged states parents-before-children, the order a pull
// must insert them in: foreign keys are on and parent_id is ON DELETE RESTRICT.
// It is the mirror of deletionOrder in history.go.
func insertOrder(states map[string]remote.State) ([]remote.State, error) {
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]remote.State, 0, len(states))
	placed := make(map[string]bool, len(states))

	var place func(id string, depth int) error
	place = func(id string, depth int) error {
		if placed[id] {
			return nil
		}
		if depth > len(states) {
			return fmt.Errorf("cycle in state graph at %s", id)
		}
		s := states[id]
		if s.Parent != "" {
			if err := place(s.Parent, depth+1); err != nil {
				return err
			}
		}
		placed[id] = true
		out = append(out, s)
		return nil
	}

	for _, id := range ids {
		if err := place(id, 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// deleteOrder returns the ids in del ordered children-before-parents, so the
// ON DELETE RESTRICT self-foreign-key on states.parent_id is never violated.
// Depth within the local graph gives the order directly: a child is always
// deeper than its parent.
func deleteOrder(local map[string]remote.State, del map[string]struct{}) []string {
	depth := func(id string) int {
		d := 0
		for cur, ok := local[id], true; ok && cur.Parent != ""; cur, ok = local[cur.Parent] {
			d++
			if d > len(local) {
				break // defensive: a cycle must not spin
			}
		}
		return d
	}

	ids := make([]string, 0, len(del))
	for id := range del {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		di, dj := depth(ids[i]), depth(ids[j])
		if di != dj {
			return di > dj // deepest first
		}
		return ids[i] < ids[j]
	})
	return ids
}
