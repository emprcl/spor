package core

import (
	"context"
	"fmt"
	"time"
)

// StateInfo is one state as seen by read commands like log. Parent is empty for
// a root state.
type StateInfo struct {
	ID        string
	CreatedAt time.Time
	Parent    string
	Label     string
}

// LogResult is the full state graph plus the current HEAD (empty if the project
// has no states yet).
type LogResult struct {
	States []StateInfo
	Head   string
}

// Log returns every state and the current HEAD, for rendering the history tree.
// It is a pure read: no write lock is taken. See docs/design-spec.md §6.
func (e *Engine) Log(ctx context.Context) (LogResult, error) {
	rows, err := e.q.ListStates(ctx)
	if err != nil {
		return LogResult{}, fmt.Errorf("listing states: %w", err)
	}
	states := make([]StateInfo, 0, len(rows))
	for _, r := range rows {
		states = append(states, StateInfo{
			ID:        r.ID,
			CreatedAt: time.UnixMilli(r.CreatedAt),
			Parent:    r.ParentID.String, // "" when NULL (root)
			Label:     r.Label.String,
		})
	}

	head, err := e.q.GetHead(ctx)
	if err != nil {
		return LogResult{}, fmt.Errorf("reading HEAD: %w", err)
	}

	return LogResult{States: states, Head: head.String}, nil
}
