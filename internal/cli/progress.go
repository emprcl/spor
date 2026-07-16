package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// Snapshot-progress helpers shared by the two front-ends that report indexing
// progress: the `spor watch` live view (a full-screen frame) and `spor snap` (a
// single stderr line). Only the wording and number formatting are shared here;
// each renderer draws in its own medium.

// indexingText is the label for a running snapshot's file-indexing progress. It
// lives in one place so the two displays never drift in wording or number
// format. A total of 0 (nothing counted yet) yields the bare label.
func indexingText(done, total int) string {
	if total == 0 {
		return "indexing project..."
	}
	return fmt.Sprintf("indexing project... %s / %s files", humanCount(done), humanCount(total))
}

// humanCount formats a non-negative count with thousands separators
// (1234 → "1,234"), so the indexing displays read cleanly on big projects.
func humanCount(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
