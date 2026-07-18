package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newVerifyCmd builds `spor verify`, the store integrity check (docs/design-spec.md §8).
// It prints any problems and exits non-zero when the store is not intact.
func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Check the stored history for corruption",
		Long: "Check the store's integrity: that every snapshot's files are present and " +
			"intact, and that the history itself is well-formed. Reports any problems " +
			"and exits non-zero if the store is not intact.",
		Example: `  # Check the store for corruption
  spor verify`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			eng, err := core.OpenForRepair(ctx, root)
			if err != nil {
				return err
			}
			defer eng.Close()

			res, err := eng.Verify(ctx)
			if err != nil {
				return err
			}
			renderVerify(styledOut(cmd), res)
			if !res.OK() {
				// Non-zero exit for scripts; the details are already printed above.
				return fmt.Errorf("verification found %d %s",
					len(res.Issues), textfmt.Plural(len(res.Issues), "problem", "problems"))
			}
			return nil
		},
	}
}

// renderVerify prints the check summary and any issues found.
func renderVerify(w io.Writer, res core.VerifyResult) {
	summary := fmt.Sprintf("%d %s, %d %s",
		res.StatesChecked, textfmt.Plural(res.StatesChecked, "snapshot", "snapshots"),
		res.BlobsChecked, textfmt.Plural(res.BlobsChecked, "blob", "blobs"))

	if res.OK() {
		fmt.Fprintln(w, th.VerifyOK.Render("✓ store is intact")+"  "+th.StatusKey.Render("("+summary+" checked)"))
		return
	}

	fmt.Fprintln(w, th.StatusKey.Render("checked "+summary))
	fmt.Fprintln(w)
	for _, iss := range res.Issues {
		loc := ""
		if iss.State != "" {
			loc = "  " + th.StatusKey.Render("["+textfmt.Abbrev(iss.State)+"]")
		}
		if iss.Path != "" {
			loc += " " + th.StatusKey.Render(iss.Path)
		}
		fmt.Fprintln(w, th.VerifyBad.Render("✗ "+iss.Detail)+loc)
	}
}
