package cli

import (
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
)

// withPager renders through a terminal pager (like git) when out is a terminal,
// so long output can be scrolled, and writes directly otherwise. render is given
// a writer whose ANSI color is reconciled to the terminal, or stripped when the
// destination is not a terminal (a pipe, a file, or a test buffer), so piped
// output never carries escape codes.
func withPager(out io.Writer, render func(io.Writer)) {
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		render(colorprofile.NewWriter(out, os.Environ()))
		return
	}

	pager := pagerCommand()
	if pager == nil {
		render(colorprofile.NewWriter(out, os.Environ()))
		return
	}

	stdin, err := pager.StdinPipe()
	if err != nil {
		render(colorprofile.NewWriter(out, os.Environ()))
		return
	}
	pager.Stdout = f
	pager.Stderr = os.Stderr
	if err := pager.Start(); err != nil {
		_ = stdin.Close()
		render(colorprofile.NewWriter(out, os.Environ()))
		return
	}

	// Color is reconciled to the real terminal even though we write into a pipe;
	// the pager (less -R) passes the escapes through. If the user quits the pager
	// early, further writes become ignored broken-pipe errors (the pipe fd is not
	// stdout/stderr, so Go returns EPIPE rather than raising SIGPIPE).
	render(&colorprofile.Writer{Forward: stdin, Profile: colorprofile.Detect(f, os.Environ())})
	_ = stdin.Close()
	// The pager's exit status is not spor's: quitting less is normal, so a
	// non-zero status is not surfaced as a command error (git behaves the same).
	_ = pager.Wait()
}

// pagerCommand builds the pager process, honoring $SPOR_PAGER then $PAGER and
// defaulting to less, the way git chooses its pager. It returns nil when paging
// is disabled (the pager is "cat" or empty) or the chosen pager is not installed,
// so the caller writes directly. The pager runs through the shell so $PAGER may
// carry flags. Unless the user set $LESS, less is configured to pass color
// through (R) and to skip paging when the output fits on one screen (F).
func pagerCommand() *exec.Cmd {
	p := os.Getenv("SPOR_PAGER")
	if p == "" {
		p = os.Getenv("PAGER")
	}
	if p == "" {
		p = defaultPager()
	}
	fields := strings.Fields(p)
	if len(fields) == 0 || fields[0] == "cat" {
		return nil
	}
	if _, err := exec.LookPath(fields[0]); err != nil {
		return nil
	}
	cmd := shellCommand(p)
	if _, ok := os.LookupEnv("LESS"); !ok {
		cmd.Env = append(os.Environ(), "LESS=FRX")
	}
	return cmd
}
