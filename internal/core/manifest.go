package core

import (
	"bytes"
	"fmt"
)

// A state's manifest travels over sync as an ordinary blob (docs/design-spec.md
// §7). Its serialization is exactly the byte stream hashManifest already digests,
// so the blob's content hash *is* the state's manifest_hash: manifests need no
// endpoint of their own, dedup across states for free, and verify themselves on
// arrival. See hashManifest in snap.go, which this must stay in step with;
// TestSerializedManifestMatchesHash pins that.

// serializeManifest renders entries in the canonical form "path\0hash\0mode\n",
// where mode is the execute bit. Entries must already be in sorted path order.
func serializeManifest(entries []manifestEntry) []byte {
	var buf bytes.Buffer
	for _, e := range entries {
		buf.WriteString(e.path)
		buf.WriteByte(0)
		buf.WriteString(e.hash)
		buf.WriteByte(0)
		buf.WriteByte('0' + byte(boolToInt(e.exec)))
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// parseManifest reads back what serializeManifest wrote.
//
// It scans field by field rather than splitting on newlines: a path may itself
// contain a newline, and only the NUL separators are guaranteed not to appear in
// one. Splitting on '\n' would corrupt such a manifest instead of rejecting it.
func parseManifest(b []byte) ([]manifestEntry, error) {
	var entries []manifestEntry
	for len(b) > 0 {
		pathEnd := bytes.IndexByte(b, 0)
		if pathEnd < 0 {
			return nil, fmt.Errorf("manifest truncated: no separator after path")
		}
		path := string(b[:pathEnd])
		b = b[pathEnd+1:]

		hashEnd := bytes.IndexByte(b, 0)
		if hashEnd < 0 {
			return nil, fmt.Errorf("manifest truncated: no separator after hash for %q", path)
		}
		hash := string(b[:hashEnd])
		b = b[hashEnd+1:]

		if len(b) < 2 {
			return nil, fmt.Errorf("manifest truncated: no mode for %q", path)
		}
		var exec bool
		switch b[0] {
		case '0':
			exec = false
		case '1':
			exec = true
		default:
			return nil, fmt.Errorf("manifest has invalid mode %q for %q", b[0], path)
		}
		if b[1] != '\n' {
			return nil, fmt.Errorf("manifest entry for %q is not newline-terminated", path)
		}
		b = b[2:]

		entries = append(entries, manifestEntry{path: path, hash: hash, exec: exec})
	}
	return entries, nil
}
