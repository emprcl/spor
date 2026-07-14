package core

import (
	"context"
	"errors"
	"testing"
)

func TestOpenExistingRequiresStore(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenExisting(context.Background(), dir); !errors.Is(err, ErrNotProject) {
		t.Fatalf("OpenExisting on a fresh dir = %v, want ErrNotProject", err)
	}
}

func TestLogReturnsStatesAndHead(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f.txt", "1")
	s1, err := eng.Snap(ctx, SnapOptions{Label: "one"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "f.txt", "2")
	s2, err := eng.Snap(ctx, SnapOptions{})
	if err != nil {
		t.Fatal(err)
	}

	res, err := eng.Log(ctx)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(res.States) != 2 {
		t.Fatalf("got %d states, want 2", len(res.States))
	}
	if res.Head != s2.StateID {
		t.Fatalf("HEAD = %s, want %s", res.Head, s2.StateID)
	}
	for _, st := range res.States {
		switch st.ID {
		case s1.StateID:
			if st.Parent != "" {
				t.Errorf("s1 should be a root, got parent %q", st.Parent)
			}
			if st.Label != "one" {
				t.Errorf("s1 label = %q, want %q", st.Label, "one")
			}
		case s2.StateID:
			if st.Parent != s1.StateID {
				t.Errorf("s2 parent = %q, want %s", st.Parent, s1.StateID)
			}
		}
	}
}
