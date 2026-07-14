package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
)

// LabelResult reports which state was named.
type LabelResult struct {
	StateID string
	Name    string
}

// LabelInfo is one label and the state it names, for listing.
type LabelInfo struct {
	Name      string
	StateID   string
	CreatedAt time.Time
}

// ListLabels returns every labeled state, sorted by label name. It is a pure
// read: no write lock is taken.
func (e *Engine) ListLabels(ctx context.Context) ([]LabelInfo, error) {
	rows, err := e.q.ListLabels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing labels: %w", err)
	}
	labels := make([]LabelInfo, 0, len(rows))
	for _, r := range rows {
		labels = append(labels, LabelInfo{
			Name:      r.Label.String,
			StateID:   r.ID,
			CreatedAt: time.UnixMilli(r.CreatedAt),
		})
	}
	return labels, nil
}

// Label gives the state named by ref a human-readable name (docs/design-spec.md §6). A
// label is mutable metadata, part of no hash, so relabeling never creates or
// alters a state; it just overwrites the name. Reusing a name across states is
// allowed (ref resolution takes the most recent). It runs under the write lock
// like any other mutating operation.
func (e *Engine) Label(ctx context.Context, ref, name string) (LabelResult, error) {
	if name == "" {
		return LabelResult{}, errors.New("a label name is required")
	}

	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return LabelResult{}, err
	}
	defer func() { _ = wl.Release() }()

	id, err := e.Resolve(ctx, ref)
	if err != nil {
		return LabelResult{}, err
	}
	// A label is a unique alias: refuse a name already held by another state. The
	// same name on the same state is a harmless no-op.
	switch owner, err := e.labelOwner(ctx, name); {
	case err != nil:
		return LabelResult{}, err
	case owner == id:
		return LabelResult{StateID: id, Name: name}, nil
	case owner != "":
		return LabelResult{}, fmt.Errorf("label %q is already used by state %s", name, owner)
	}
	if err := e.q.SetStateLabel(ctx, gen.SetStateLabelParams{
		Label: sql.NullString{String: name, Valid: true},
		ID:    id,
	}); err != nil {
		return LabelResult{}, fmt.Errorf("setting label: %w", err)
	}
	return LabelResult{StateID: id, Name: name}, nil
}

// labelOwner returns the id of the state currently holding name, or "" if the
// name is free. Labels are unique (docs/design-spec.md §2), so there is at most one.
func (e *Engine) labelOwner(ctx context.Context, name string) (string, error) {
	id, err := e.q.GetStateIDByLabel(ctx, sql.NullString{String: name, Valid: true})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("checking label %q: %w", name, err)
	default:
		return id, nil
	}
}
