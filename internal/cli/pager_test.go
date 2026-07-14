package cli

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

// TestWithPagerNonTTYWritesDirect checks that a non-terminal destination (here a
// buffer) is written to directly, with color stripped and no pager involved.
func TestWithPagerNonTTYWritesDirect(t *testing.T) {
	var buf bytes.Buffer
	withPager(&buf, func(w io.Writer) { fmt.Fprint(w, "hello world") })
	if buf.String() != "hello world" {
		t.Fatalf("output = %q, want %q", buf.String(), "hello world")
	}
}

// TestPagerCommandDisabled checks that setting the pager to "cat" disables
// paging so the caller writes directly.
func TestPagerCommandDisabled(t *testing.T) {
	t.Setenv("SPOR_PAGER", "cat")
	if cmd := pagerCommand(); cmd != nil {
		t.Fatalf("pager 'cat' should disable paging, got %v", cmd.Args)
	}
}

// TestPagerCommandMissingBinary checks that an uninstalled pager disables paging
// rather than failing.
func TestPagerCommandMissingBinary(t *testing.T) {
	t.Setenv("SPOR_PAGER", "definitely-not-a-real-pager-binary-xyz")
	if cmd := pagerCommand(); cmd != nil {
		t.Fatalf("a missing pager should disable paging, got %v", cmd.Args)
	}
}

// TestDefaultPagerNonEmpty checks that the platform default pager is always set,
// so pagerCommand never falls through to an empty command name.
func TestDefaultPagerNonEmpty(t *testing.T) {
	if defaultPager() == "" {
		t.Fatal("defaultPager returned empty")
	}
}
