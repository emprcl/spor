package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newVerifyCmd builds `spor verify`, the store integrity check (docs/design-spec.md §8).
// It prints any problems and exits non-zero when the store is not intact.
func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Check the store's integrity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			eng, err := core.OpenExisting(ctx, root)
			if err != nil {
				return err
			}
			defer eng.Close()

			res, err := eng.Verify(ctx)
			if err != nil {
				return err
			}
			out := colorprofile.NewWriter(cmd.OutOrStdout(), os.Environ())
			renderVerify(out, res)
			if !res.OK() {
				// Non-zero exit for scripts; the details are already printed above.
				return fmt.Errorf("verification found %d %s",
					len(res.Issues), plural(len(res.Issues), "problem", "problems"))
			}
			return nil
		},
	}
}

// renderVerify prints the check summary and any issues found.
func renderVerify(w io.Writer, res core.VerifyResult) {
	summary := fmt.Sprintf("%d %s, %d %s",
		res.StatesChecked, plural(res.StatesChecked, "state", "states"),
		res.BlobsChecked, plural(res.BlobsChecked, "blob", "blobs"))

	if res.OK() {
		fmt.Fprintln(w, styleVerifyOK.Render("✓ store is intact")+"  "+styleStatusKey.Render("("+summary+" checked)"))
		return
	}

	fmt.Fprintln(w, styleStatusKey.Render("checked "+summary))
	fmt.Fprintln(w)
	for _, iss := range res.Issues {
		loc := ""
		if iss.State != "" {
			loc = "  " + styleStatusKey.Render("["+abbrev(iss.State)+"]")
		}
		if iss.Path != "" {
			loc += " " + styleStatusKey.Render(iss.Path)
		}
		fmt.Fprintln(w, styleVerifyBad.Render("✗ "+iss.Detail)+loc)
	}
}
