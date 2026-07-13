package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emprcl/spor/internal/db/gen"
)

// Resolve turns a user-facing <ref> into a concrete, opaque state id. It follows
// the precedence in docs/SPEC.md §6:
//
//  1. the @ / @~n sigils (HEAD and its nth ancestor);
//  2. an exact label match (so a label named "2h" wins over the duration);
//  3. a time ("2h ago", the word "ago" optional) resolved against @'s own
//     timeline;
//  4. a ULID prefix.
//
// It is a pure read and takes no lock. Natural-language times ("yesterday",
// "friday 3pm") are not parsed yet; only Go durations are, which covers the
// common "2h ago" form. Anything else falls through to the ULID-prefix step.
func (e *Engine) Resolve(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("empty ref")
	}

	states, err := e.q.ListStates(ctx)
	if err != nil {
		return "", fmt.Errorf("listing states: %w", err)
	}
	if len(states) == 0 {
		return "", errors.New("no states yet; nothing to resolve")
	}
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return "", fmt.Errorf("reading HEAD: %w", err)
	}

	// 1. @ and @~n walk HEAD's ancestor line.
	if ref == "@" || strings.HasPrefix(ref, "@~") {
		return resolveHeadRelative(ref, head, states)
	}
	// 2. Exact label.
	if id, ok := resolveLabel(ref, states); ok {
		return id, nil
	}
	// 3. Time: the deepest ancestor of @ at or before T.
	if t, ok := parseTimeRef(ref); ok {
		return resolveTime(t, head, states)
	}
	// 4. ULID prefix.
	return resolvePrefix(ref, states)
}

// resolveHeadRelative handles @ (n == 0) and @~n by walking n parent links up
// from HEAD. Going past the root is an error, as is a HEAD-relative ref with no
// current state.
func resolveHeadRelative(ref string, head sql.NullString, states []gen.ListStatesRow) (string, error) {
	if !head.Valid {
		return "", errors.New("no current state, @ is undefined")
	}
	n := 0
	if ref != "@" {
		v, err := strconv.Atoi(strings.TrimPrefix(ref, "@~"))
		if err != nil || v < 0 {
			return "", fmt.Errorf("invalid ref %q", ref)
		}
		n = v
	}
	byID := indexStates(states)
	cur := head.String
	for i := 0; i < n; i++ {
		s, ok := byID[cur]
		if !ok {
			return "", fmt.Errorf("state %s not found", cur)
		}
		if !s.ParentID.Valid {
			return "", fmt.Errorf("@~%d reaches past the root of history", n)
		}
		cur = s.ParentID.String
	}
	return cur, nil
}

// resolveLabel returns the id of the state carrying an exact label. If a label
// was reused across states the most recent one wins.
func resolveLabel(ref string, states []gen.ListStatesRow) (string, bool) {
	best := gen.ListStatesRow{}
	found := false
	for _, s := range states {
		if s.Label.Valid && s.Label.String == ref && (!found || s.CreatedAt > best.CreatedAt) {
			best, found = s, true
		}
	}
	return best.ID, found
}

// resolveTime rewinds @'s own timeline (docs/SPEC.md §6): it returns the deepest
// ancestor of @ created at or before t, never some abandoned branch. Creation
// times strictly increase along any ancestor chain, so this is well defined.
func resolveTime(t time.Time, head sql.NullString, states []gen.ListStatesRow) (string, error) {
	if !head.Valid {
		return "", errors.New("no current state, @ is undefined")
	}
	byID := indexStates(states)
	cutoff := t.UnixMilli()
	cur := head.String
	for {
		s, ok := byID[cur]
		if !ok {
			return "", fmt.Errorf("state %s not found", cur)
		}
		if s.CreatedAt <= cutoff {
			return s.ID, nil
		}
		if !s.ParentID.Valid {
			return "", fmt.Errorf("no state at or before that time")
		}
		cur = s.ParentID.String
	}
}

// resolvePrefix matches a ULID prefix, case-insensitively (ULIDs are canonically
// uppercase Crockford base32). It errors if the prefix matches no state or more
// than one.
func resolvePrefix(ref string, states []gen.ListStatesRow) (string, error) {
	up := strings.ToUpper(ref)
	var match string
	count := 0
	for _, s := range states {
		if strings.HasPrefix(s.ID, up) {
			match = s.ID
			count++
		}
	}
	switch count {
	case 0:
		return "", fmt.Errorf("no state matches ref %q", ref)
	case 1:
		return match, nil
	default:
		return "", fmt.Errorf("ref %q is ambiguous (%d states match); use more characters", ref, count)
	}
}

// parseTimeRef parses a duration-style time ref such as "2h ago" or "90m",
// returning the absolute instant it names. The trailing "ago" is optional (as in
// the spec's examples). It reports ok == false for anything that is not a
// non-negative Go duration, so the caller can fall through to the next ref kind.
func parseTimeRef(ref string) (time.Time, bool) {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ref), "ago"))
	if s == "" {
		return time.Time{}, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return time.Time{}, false
	}
	return time.Now().Add(-d), true
}

// indexStates keys states by id for parent-chain walks.
func indexStates(states []gen.ListStatesRow) map[string]gen.ListStatesRow {
	m := make(map[string]gen.ListStatesRow, len(states))
	for _, s := range states {
		m[s.ID] = s
	}
	return m
}
