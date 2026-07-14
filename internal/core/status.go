package core

import "context"

// StatusResult is a snapshot of the project's runtime state for `spor status`
// (docs/SPEC.md §6): whether a watcher is running and where HEAD (@) is.
type StatusResult struct {
	Root           string
	WatcherRunning bool
	HasHead        bool
	Head           StateInfo // valid when HasHead
	StateCount     int
}

// Status reports whether a watcher is running and the current HEAD. It is a pure
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
	res := StatusResult{
		Root:           e.root,
		WatcherRunning: running,
		StateCount:     len(logRes.States),
	}
	if logRes.Head != "" {
		for _, s := range logRes.States {
			if s.ID == logRes.Head {
				res.HasHead, res.Head = true, s
				break
			}
		}
	}
	return res, nil
}
