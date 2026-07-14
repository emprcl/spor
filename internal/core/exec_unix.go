//go:build !windows

package core

import (
	"context"
	"database/sql"
)

// resolveExec is a no-op on platforms where the filesystem reports the execute
// bit: walk already observed it, so a chmod +x is captured directly and this
// keeps the observed value. See resolveExec in exec_windows.go for the inheriting
// counterpart, and docs/design-spec.md §4.
func (e *Engine) resolveExec(_ context.Context, _ sql.NullString, _ []manifestEntry) error {
	return nil
}
