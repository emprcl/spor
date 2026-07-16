package cli

import (
	"io"
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"
)

// styledOut is the single place a command gets its output sink: it wraps stdout
// in a colorprofile writer that renders lipgloss ANSI at full fidelity on a
// terminal and strips it when the destination is a pipe, a file, or a test
// buffer. Styled output therefore degrades to plain automatically, so a script
// (id=$(spor snap)) or a pipe never sees escape codes.
func styledOut(cmd *cobra.Command) io.Writer {
	return colorprofile.NewWriter(cmd.OutOrStdout(), os.Environ())
}
