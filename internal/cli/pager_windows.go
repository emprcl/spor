//go:build windows

package cli

import "os/exec"

// defaultPager prefers less when it is installed (Git for Windows ships one) and
// otherwise falls back to more, the pager present on stock Windows.
func defaultPager() string {
	if _, err := exec.LookPath("less"); err == nil {
		return "less"
	}
	return "more"
}

// shellCommand wraps the pager invocation in cmd.exe so a $PAGER value may carry
// flags, mirroring the shell handling on Unix. cmd.exe strips the surrounding
// quotes Go adds around a command line that contains spaces, so "less -R" runs
// as expected.
func shellCommand(p string) *exec.Cmd {
	return exec.Command("cmd", "/c", p) //nolint:gosec // the pager comes from the user's own env, as in git
}
