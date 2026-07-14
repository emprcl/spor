package core

import "context"

// StatusResult is a snapshot of the project's runtime state for `spor status`
// (docs/design-spec.md §6): whether a watcher is running, how big the history is, and
// where HEAD (@) sits within it.
type StatusResult struct {
	Root           string
	WatcherRunning bool
	HasHead        bool
	Head           StateInfo // valid when HasHead
	StateCount     int
	Tips           int   // leaf states, i.e. distinct current timelines
	Ahead          int   // states below @ (newer); >0 means you rewound into the past
	StoreBytes     int64 // total on-disk size of the .spor store
}

// Status reports the watcher state, history size, and where HEAD is. It is a pure
// read and takes no write lock (the watcher check only probes the lock).
func (e *Engine) Status(ctx context.Context) (StatusResult, error) {
	running, err := e.WatcherRunning()
	if err != nil {
		return StatusResult{}, err
	}
	logRes, err := e.Log(ctx)
	if err != nil {
		return StatusResult{}, err
	}
	size, err := dirSize(e.storeDir)
	if err != nil {
		return StatusResult{}, err
	}

	res := StatusResult{
		Root:           e.root,
		WatcherRunning: running,
		StateCount:     len(logRes.States),
		StoreBytes:     size,
	}

	// Index children so leaves (timelines) and HEAD's descendants are countable.
	present := make(map[string]struct{}, len(logRes.States))
	for _, s := range logRes.States {
		present[s.ID] = struct{}{}
	}
	children := make(map[string][]string)
	hasChild := make(map[string]bool)
	for _, s := range logRes.States {
		if _, ok := present[s.Parent]; ok {
			children[s.Parent] = append(children[s.Parent], s.ID)
			hasChild[s.Parent] = true
		}
	}
	for _, s := range logRes.States {
		if !hasChild[s.ID] {
			res.Tips++
		}
	}

	if logRes.Head != "" {
		for _, s := range logRes.States {
			if s.ID == logRes.Head {
				res.HasHead, res.Head = true, s
				break
			}
		}
		res.Ahead = countDescendants(children, logRes.Head)
	}
	return res, nil
}

// countDescendants returns how many states lie below root in the child index.
func countDescendants(children map[string][]string, root string) int {
	n := 0
	var walk func(id string)
	walk = func(id string) {
		for _, c := range children[id] {
			n++
			walk(c)
		}
	}
	walk(root)
	return n
}
