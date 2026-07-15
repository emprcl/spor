package core

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/emprcl/spor/internal/db/gen"
)

// TestResolveHeadAndAncestors covers @ and @~n along HEAD's ancestor line.
func TestResolveHeadAndAncestors(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	a := snapID(t, eng)
	write(t, root, "f", "2")
	b := snapID(t, eng)
	write(t, root, "f", "3")
	c := snapID(t, eng)

	cases := map[string]string{"@": c, "@~0": c, "@~1": b, "@~2": a}
	for ref, want := range cases {
		got, err := eng.Resolve(ctx, ref)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", ref, err)
		}
		if got != want {
			t.Fatalf("Resolve(%q) = %s, want %s", ref, got, want)
		}
	}

	if _, err := eng.Resolve(ctx, "@~3"); err == nil {
		t.Fatal("Resolve(@~3) past the root should error")
	}
}

// TestResolveLabelBeatsTime checks precedence: an exact label wins over a value
// that would otherwise parse as a duration.
func TestResolveLabelBeatsTime(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	res, err := eng.Snap(ctx, SnapOptions{Label: "2h"})
	if err != nil {
		t.Fatalf("Snap: %v", err)
	}

	got, err := eng.Resolve(ctx, "2h")
	if err != nil {
		t.Fatalf("Resolve(2h): %v", err)
	}
	if got != res.StateID {
		t.Fatalf("Resolve(2h) = %s, want the labeled state %s", got, res.StateID)
	}
}

// TestResolveTimeIsNowRelative checks the wall-clock plumbing: "0s" is now,
// so it resolves to HEAD, while a cutoff before any state exists errors.
func TestResolveTimeIsNowRelative(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	b := snapID(t, eng)

	if got, err := eng.Resolve(ctx, "0s"); err != nil || got != b {
		t.Fatalf("Resolve(0s) = %s (err %v), want HEAD %s", got, err, b)
	}
	// A cutoff an hour before these just-created states matches nothing.
	if _, err := eng.Resolve(ctx, "1h"); err == nil {
		t.Fatal("Resolve(1h) with only just-created states should error")
	}
}

// TestResolveTimeRewindsTimeline exercises the ancestor walk directly with
// controlled timestamps, avoiding wall-clock flakiness: the deepest ancestor of
// HEAD at or before the cutoff is chosen, and side branches are ignored.
func TestResolveTimeRewindsTimeline(t *testing.T) {
	// Chain root(100) -> mid(200) -> head(300), plus a side branch off root at
	// t=250 that must never be selected when rewinding HEAD's own line.
	states := []gen.ListStatesRow{
		{ID: "root", CreatedAt: 100},
		{ID: "mid", CreatedAt: 200, ParentID: sql.NullString{String: "root", Valid: true}},
		{ID: "head", CreatedAt: 300, ParentID: sql.NullString{String: "mid", Valid: true}},
		{ID: "side", CreatedAt: 250, ParentID: sql.NullString{String: "root", Valid: true}},
	}
	head := sql.NullString{String: "head", Valid: true}

	cases := []struct {
		cutoff int64
		want   string
	}{
		{cutoff: 300, want: "head"},
		{cutoff: 299, want: "mid"},
		{cutoff: 250, want: "mid"}, // side(250) is off HEAD's line, so mid wins
		{cutoff: 150, want: "root"},
	}
	for _, c := range cases {
		got, err := resolveTime(time.UnixMilli(c.cutoff), head, states)
		if err != nil {
			t.Fatalf("resolveTime(%d): %v", c.cutoff, err)
		}
		if got != c.want {
			t.Fatalf("resolveTime(%d) = %s, want %s", c.cutoff, got, c.want)
		}
	}
	if _, err := resolveTime(time.UnixMilli(50), head, states); err == nil {
		t.Fatal("resolveTime before the root should error")
	}
}

// TestParseTimeRef covers the accepted time units (s, m, h, d) and rejection of
// non-time refs so they fall through to the id-prefix step.
func TestParseTimeRef(t *testing.T) {
	now := time.Now()
	cases := []struct {
		ref  string
		back time.Duration // how far before now it should name
		ok   bool
	}{
		{"0s", 0, true},
		{"45s", 45 * time.Second, true},
		{"90m", 90 * time.Minute, true},
		{"2h", 2 * time.Hour, true},
		{"1h30m", 90 * time.Minute, true},
		{"3d", 3 * 24 * time.Hour, true},
		{"1.5d", 36 * time.Hour, true},
		{"yesterday", 0, false},
		{"2h ago", 0, false}, // the "ago" suffix is not part of the grammar
		{"", 0, false},
		{"01ARZ", 0, false}, // a ULID-ish prefix is not a time
		{"5", 0, false},     // a bare number has no unit
	}
	for _, c := range cases {
		got, ok := parseTimeRef(c.ref)
		if ok != c.ok {
			t.Fatalf("parseTimeRef(%q) ok = %v, want %v", c.ref, ok, c.ok)
		}
		if !ok {
			continue
		}
		want := now.Add(-c.back)
		if d := got.Sub(want); d > time.Second || d < -time.Second {
			t.Fatalf("parseTimeRef(%q) = %v, want ~%v", c.ref, got, want)
		}
	}
}

// TestResolvePrefix covers ULID-prefix matching and its error cases.
func TestResolvePrefix(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	a := snapID(t, eng)

	// A full id and a lowercase prefix both resolve.
	if got, err := eng.Resolve(ctx, a); err != nil || got != a {
		t.Fatalf("Resolve(full id) = %s (err %v), want %s", got, err, a)
	}
	if got, err := eng.Resolve(ctx, strings.ToLower(a[:8])); err != nil || got != a {
		t.Fatalf("Resolve(lowercase prefix): got %s err %v", got, err)
	}
	if _, err := eng.Resolve(ctx, "ZZZZZZ"); err == nil {
		t.Fatal("Resolve of a non-matching prefix should error")
	}
}
