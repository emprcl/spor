//go:build windows

package core

import (
	"context"
	"database/sql"
	"fmt"
)

// resolveExec inherits the execute bit from the parent state on Windows, where
// the filesystem cannot report it. Without this, every snapshot would read the
// bit as false and, because it is folded into the manifest hash, flip inherited
// executable files back off as a spurious new state. Files absent from the parent
// (new paths) keep their observed value, which is false, so the bit can only be
// set through an explicit future command. See docs/design-spec.md §4.
func (e *Engine) resolveExec(ctx context.Context, parent sql.NullString, entries []manifestEntry) error {
	if !parent.Valid {
		return nil // first snapshot: nothing to inherit
	}
	rows, err := e.q.ListManifestEntries(ctx, parent.String)
	if err != nil {
		return fmt.Errorf("reading parent manifest: %w", err)
	}
	prev := make(map[string]bool, len(rows))
	for _, r := range rows {
		prev[r.Path] = r.Executable != 0
	}
	for i := range entries {
		if v, ok := prev[entries[i].path]; ok {
			entries[i].exec = v
		}
	}
	return nil
}
