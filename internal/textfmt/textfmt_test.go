package textfmt

import (
	"testing"
	"time"
)

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
		if got := HumanBytes(c.n); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestHumanizeSince(t *testing.T) {
	now := time.Now()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "10s ago"},
		{5 * time.Minute, " 5m ago"},
		{59 * time.Minute, "59m ago"},
		{3 * time.Hour, " 3h ago"},
		{50 * time.Hour, " 2d ago"},
		{9 * 7 * 24 * time.Hour, " 9w ago"},
		{400 * 24 * time.Hour, " 1y ago"},
	}
	for _, c := range cases {
		if got := HumanizeSince(now.Add(-c.d)); got != c.want {
			t.Errorf("HumanizeSince(-%s) = %q, want %q", c.d, got, c.want)
		}
		if len(c.want) != TimeFieldWidth {
			t.Errorf("case %s want %q has width %d, want fixed %d", c.d, c.want, len(c.want), TimeFieldWidth)
		}
	}
}
