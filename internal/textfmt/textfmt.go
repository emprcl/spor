// Package textfmt holds the small, pure presentation formatters shared by the CLI
// and the interactive TUI: id abbreviation, pluralization, byte and relative-time
// rendering. Keeping them here lets `spor log`, `spor status` and the watch TUI read
// the same without either package depending on the other.
package textfmt

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Abbrev shortens a state id for headers and one-line notes.
func Abbrev(id string) string {
	const n = 10
	if len(id) > n {
		return id[:n]
	}
	return id
}

// Plural picks the singular or plural word for a count.
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// HumanBytes formats a byte count with a binary (1024) unit suffix.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// The snapshot-progress labels live here so every front-end that reports them,
// `spor snap`, the `spor watch` live TUI, and the plain watch log, never drifts
// in wording or number format. One label per core.SnapPhase.

// ScanningText is the label for the walk phase. A count of 0 (nothing found
// yet) yields the bare label.
func ScanningText(found int) string {
	if found == 0 {
		return "scanning project..."
	}
	return fmt.Sprintf("scanning project... %s files", HumanCount(found))
}

// IndexingText is the label for a running snapshot's file-indexing progress. A
// total of 0 (nothing counted yet) yields the bare label.
func IndexingText(done, total int) string {
	if total == 0 {
		return "indexing project..."
	}
	return fmt.Sprintf("indexing project... %s / %s files", HumanCount(done), HumanCount(total))
}

// SyncingText is the label for the whole-store durability sync.
func SyncingText() string { return "syncing to disk..." }

// SavingText is the label for the state-commit phase. A total of 0 yields the
// bare label.
func SavingText(done, total int) string {
	if total == 0 {
		return "saving snapshot..."
	}
	return fmt.Sprintf("saving snapshot... %s / %s files", HumanCount(done), HumanCount(total))
}

// HumanCount formats a non-negative count with thousands separators
// (1234 → "1,234"), so the indexing displays read cleanly on big projects.
func HumanCount(n int) string {
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

// TimeFieldWidth is the fixed display width of every HumanizeSince value. The
// widest cases ("59s ago", "59m ago", "23h ago", "51w ago") are 7 columns, and
// every shorter value is right-aligned into that width so the number, unit, and
// "ago" always land in the same place.
const TimeFieldWidth = 7

// HumanizeSince renders a fixed-width relative time: "now", or "<n><unit> ago"
// with a one-letter unit (s, m, h, d, w, y). Every value is exactly
// TimeFieldWidth columns wide, right-aligned, so the log's time column stays
// perfectly uniform regardless of the age.
func HumanizeSince(t time.Time) string {
	d := time.Since(t)
	var core string
	switch {
	case d < time.Minute && int(d.Seconds()) == 0:
		core = "now"
	case d < time.Minute:
		core = fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		core = fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		core = fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		core = fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		core = fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		core = fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
	return fmt.Sprintf("%*s", TimeFieldWidth, core)
}
