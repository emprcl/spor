//go:build !windows

package cli

import "os/exec"

// defaultPager is the pager used when neither $SPOR_PAGER nor $PAGER is set.
func defaultPager() string { return "less" }

// shellCommand wraps the pager invocation in the platform shell so a $PAGER
// value may carry flags (e.g. "less -R"), the way git runs its pager.
func shellCommand(p string) *exec.Cmd {
	return exec.Command("sh", "-c", p) //nolint:gosec // the pager comes from the user's own env, as in git
}
