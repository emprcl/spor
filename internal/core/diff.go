package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/emprcl/spor/internal/db/gen"
)

// Diffs are not stored; they are computed on demand by comparing two states'
// manifests and blobs: a line diff when both sides are text, a coarse report
// otherwise (docs/design-spec.md §5). Diff always compares two points in history, never
// the working tree (§6).

const (
	maxDiffBytes = 8 << 20    // per side; larger text is reported coarsely, not line-diffed
	sniffBytes   = 8000       // NUL-scan window for binary detection, as in Git
	maxDiffCells = 16_000_000 // LCS table cap (len(a)*len(b)); larger is reported coarsely
	diffContext  = 3          // lines of context around each change
)

// ChangeKind classifies how a path differs between two states.
type ChangeKind int

const (
	Added ChangeKind = iota
	Removed
	Modified
)

// LineOp classifies a single line within a hunk.
type LineOp int

const (
	OpContext LineOp = iota
	OpDel
	OpAdd
)

// DiffLine is one line of a hunk: unchanged context, a deletion, or an addition.
type DiffLine struct {
	Op   LineOp
	Text string
}

// Hunk is a contiguous run of changes plus surrounding context, with the 1-based
// start line and line count on each side (the numbers in a `@@ -o,ol +n,nl @@`
// header).
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []DiffLine
}

// FileDiff is the change to one path. Hunks is empty when the change cannot be
// shown as a line diff: a binary file, a file too large to diff, an added or
// removed empty file, or a change to the execute bit alone (see ModeOnly).
type FileDiff struct {
	Path      string
	Kind      ChangeKind
	Binary    bool // content differs but a side is binary (NUL detected)
	Truncated bool // text too large to line-diff
	OldExec   bool // execute bit on the "from" side (false when the path is absent)
	NewExec   bool // execute bit on the "to" side (false when the path is absent)
	Hunks     []Hunk
}

// ModeOnly reports a modification that is only the execute bit: same content on
// both sides, so there is nothing to line-diff.
func (f FileDiff) ModeOnly() bool {
	return f.Kind == Modified && !f.Binary && !f.Truncated && len(f.Hunks) == 0 &&
		f.OldExec != f.NewExec
}

// DiffResult is the change set between two resolved states.
type DiffResult struct {
	From  string // resolved id of the "from" side
	To    string // resolved id of the "to" side
	Files []FileDiff
}

// Diff computes the changes from state refFrom to state refTo. Both refs are
// resolved with the usual precedence (docs/design-spec.md §6). It is a pure read and
// takes no lock. Files are returned sorted by path; unchanged paths are omitted.
func (e *Engine) Diff(ctx context.Context, refFrom, refTo string) (DiffResult, error) {
	from, err := e.Resolve(ctx, refFrom)
	if err != nil {
		return DiffResult{}, err
	}
	to, err := e.Resolve(ctx, refTo)
	if err != nil {
		return DiffResult{}, err
	}

	fromRows, err := e.q.ListManifestEntries(ctx, from)
	if err != nil {
		return DiffResult{}, fmt.Errorf("reading manifest of %s: %w", from, err)
	}
	toRows, err := e.q.ListManifestEntries(ctx, to)
	if err != nil {
		return DiffResult{}, fmt.Errorf("reading manifest of %s: %w", to, err)
	}
	fromMap := indexManifest(fromRows)
	toMap := indexManifest(toRows)

	paths := unionPaths(fromMap, toMap)
	sort.Strings(paths)

	files := make([]FileDiff, 0, len(paths))
	for _, p := range paths {
		a, inA := fromMap[p]
		b, inB := toMap[p]
		switch {
		case inA && !inB:
			fd, err := e.diffOneSided(p, a, Removed)
			if err != nil {
				return DiffResult{}, err
			}
			files = append(files, fd)
		case !inA && inB:
			fd, err := e.diffOneSided(p, b, Added)
			if err != nil {
				return DiffResult{}, err
			}
			files = append(files, fd)
		case a.BlobHash == b.BlobHash:
			if a.Executable != b.Executable {
				files = append(files, FileDiff{
					Path: p, Kind: Modified,
					OldExec: a.Executable != 0, NewExec: b.Executable != 0,
				})
			}
			// identical content and mode: no change, omit.
		default:
			fd, err := e.diffModified(p, a, b)
			if err != nil {
				return DiffResult{}, err
			}
			files = append(files, fd)
		}
	}
	return DiffResult{From: from, To: to, Files: files}, nil
}

// diffOneSided builds the diff for a path present on only one side: a whole-file
// addition or removal. A binary or oversized blob is reported coarsely.
func (e *Engine) diffOneSided(path string, ent gen.ListManifestEntriesRow, kind ChangeKind) (FileDiff, error) {
	content, binary, truncated, err := e.loadBlob(ent.BlobHash)
	if err != nil {
		return FileDiff{}, err
	}
	fd := FileDiff{Path: path, Kind: kind}
	if kind == Added {
		fd.NewExec = ent.Executable != 0
	} else {
		fd.OldExec = ent.Executable != 0
	}
	switch {
	case binary:
		fd.Binary = true
	case truncated:
		fd.Truncated = true
	default:
		op := OpAdd
		if kind == Removed {
			op = OpDel
		}
		if lines := splitLines(content); len(lines) > 0 {
			fd.Hunks = []Hunk{wholeHunk(lines, op)}
		}
	}
	return fd, nil
}

// diffModified builds the diff for a path whose content changed between sides.
func (e *Engine) diffModified(path string, a, b gen.ListManifestEntriesRow) (FileDiff, error) {
	fd := FileDiff{
		Path: path, Kind: Modified,
		OldExec: a.Executable != 0, NewExec: b.Executable != 0,
	}
	ca, binA, truncA, err := e.loadBlob(a.BlobHash)
	if err != nil {
		return FileDiff{}, err
	}
	cb, binB, truncB, err := e.loadBlob(b.BlobHash)
	if err != nil {
		return FileDiff{}, err
	}
	switch {
	case binA || binB:
		fd.Binary = true
	case truncA || truncB:
		fd.Truncated = true
	default:
		hunks, ok := lineDiff(splitLines(ca), splitLines(cb))
		if !ok {
			fd.Truncated = true
		} else {
			fd.Hunks = hunks
		}
	}
	return fd, nil
}

// loadBlob reads a blob's plaintext for diffing, up to maxDiffBytes. It reports
// whether the content is binary (a NUL byte in the sniff window) and whether it
// was truncated at the size cap.
func (e *Engine) loadBlob(hash string) (content []byte, binary, truncated bool, err error) {
	r, err := e.blobs.Open(hash)
	if err != nil {
		return nil, false, false, fmt.Errorf("opening blob %s: %w", hash, err)
	}
	defer r.Close()

	buf, err := io.ReadAll(io.LimitReader(r, maxDiffBytes+1))
	if err != nil {
		return nil, false, false, fmt.Errorf("reading blob %s: %w", hash, err)
	}
	if len(buf) > maxDiffBytes {
		truncated = true
		buf = buf[:maxDiffBytes]
	}
	return buf, isBinary(buf), truncated, nil
}

// isBinary reports whether the sniff window contains a NUL byte, the same signal
// Git uses to classify a blob as binary.
func isBinary(b []byte) bool {
	if len(b) > sniffBytes {
		b = b[:sniffBytes]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// splitLines splits content into lines, dropping a single trailing newline so a
// normally-terminated file does not diff as having a trailing empty line.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.Split(s, "\n")
}

// wholeHunk turns every line of an added or removed file into one hunk.
func wholeHunk(lines []string, op LineOp) Hunk {
	h := Hunk{Lines: make([]DiffLine, 0, len(lines))}
	for _, ln := range lines {
		h.Lines = append(h.Lines, DiffLine{Op: op, Text: ln})
	}
	if op == OpAdd {
		h.NewStart, h.NewLines = 1, len(lines)
	} else {
		h.OldStart, h.OldLines = 1, len(lines)
	}
	return h
}

func indexManifest(rows []gen.ListManifestEntriesRow) map[string]gen.ListManifestEntriesRow {
	m := make(map[string]gen.ListManifestEntriesRow, len(rows))
	for _, r := range rows {
		m[r.Path] = r
	}
	return m
}

func unionPaths(a, b map[string]gen.ListManifestEntriesRow) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for p := range a {
		seen[p] = struct{}{}
	}
	for p := range b {
		seen[p] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}

// scriptLine is one entry of the full edit script, carrying the 1-based line
// number on each side so hunks can be labeled.
type scriptLine struct {
	op             LineOp
	text           string
	oldNum, newNum int
}

// lineDiff computes the hunks turning a into b. It reports ok == false when the
// inputs are too large to diff (the LCS table would exceed maxDiffCells), so the
// caller can fall back to a coarse report.
func lineDiff(a, b []string) (hunks []Hunk, ok bool) {
	if len(a)*len(b) > maxDiffCells {
		return nil, false
	}
	return hunkize(lcsScript(a, b), diffContext), true
}

// lcsScript computes a longest-common-subsequence edit script over lines: a
// backward DP table, then a forward walk emitting context, deletions, and
// additions in order.
func lcsScript(a, b []string) []scriptLine {
	n, m := len(a), len(b)
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var out []scriptLine
	i, j, oldNum, newNum := 0, 0, 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			oldNum++
			newNum++
			out = append(out, scriptLine{OpContext, a[i], oldNum, newNum})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			oldNum++
			out = append(out, scriptLine{OpDel, a[i], oldNum, newNum})
			i++
		default:
			newNum++
			out = append(out, scriptLine{OpAdd, b[j], oldNum, newNum})
			j++
		}
	}
	for ; i < n; i++ {
		oldNum++
		out = append(out, scriptLine{OpDel, a[i], oldNum, newNum})
	}
	for ; j < m; j++ {
		newNum++
		out = append(out, scriptLine{OpAdd, b[j], oldNum, newNum})
	}
	return out
}

// hunkize groups an edit script into hunks, keeping ctx lines of context around
// each change and merging changes whose context windows touch. A script with no
// changes yields no hunks.
func hunkize(script []scriptLine, ctx int) []Hunk {
	include := make([]bool, len(script))
	changed := false
	for i, s := range script {
		if s.op == OpContext {
			continue
		}
		changed = true
		lo, hi := i-ctx, i+ctx
		if lo < 0 {
			lo = 0
		}
		if hi >= len(script) {
			hi = len(script) - 1
		}
		for j := lo; j <= hi; j++ {
			include[j] = true
		}
	}
	if !changed {
		return nil
	}

	var hunks []Hunk
	for i := 0; i < len(script); {
		if !include[i] {
			i++
			continue
		}
		j := i
		for j < len(script) && include[j] {
			j++
		}
		hunks = append(hunks, buildHunk(script[i:j]))
		i = j
	}
	return hunks
}

// buildHunk turns a contiguous script segment into a Hunk, deriving each side's
// start line and count from the first real line on that side.
func buildHunk(seg []scriptLine) Hunk {
	h := Hunk{Lines: make([]DiffLine, 0, len(seg))}
	oldSet, newSet := false, false
	for _, s := range seg {
		h.Lines = append(h.Lines, DiffLine{Op: s.op, Text: s.text})
		if s.op != OpAdd {
			h.OldLines++
			if !oldSet {
				h.OldStart, oldSet = s.oldNum, true
			}
		}
		if s.op != OpDel {
			h.NewLines++
			if !newSet {
				h.NewStart, newSet = s.newNum, true
			}
		}
	}
	if !oldSet {
		h.OldStart = seg[0].oldNum // pure additions: line before which they insert
	}
	if !newSet {
		h.NewStart = seg[0].newNum // pure deletions
	}
	return h
}
