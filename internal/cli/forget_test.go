package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPromptYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"", false},        // EOF, no input
		{"maybe\n", false}, // anything else is no
	}
	for _, c := range cases {
		var out bytes.Buffer
		got := promptYesNo(strings.NewReader(c.in), &out, "Delete?")
		if got != c.want {
			t.Errorf("promptYesNo(%q) = %v, want %v", c.in, got, c.want)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Errorf("prompt did not show [y/N] default: %q", out.String())
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// verify promptYesNo takes an io.Reader (documents the seam used for testing).
var _ func(io.Reader, io.Writer, string) bool = promptYesNo
