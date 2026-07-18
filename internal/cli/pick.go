package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newPickCmd builds `spor pick <ref> <path>`, which brings one file (or
// one directory) back from a past snapshot without moving the rest of the
// project (docs/design-spec.md §5, §6).
func newPickCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pick <ref> <path>",
		Short: "Bring back one file from a past snapshot",
		Long: "Copy a single file, or a directory, out of a past snapshot into your " +
			"working tree, leaving every other file as it is and without moving you in " +
			"history. Any changes you had not snapshotted yet are recorded first, and " +
			"the picked result is recorded as a new snapshot, so a pick is always " +
			"reversible. Nothing is ever deleted by a pick.\n\n" +
			"A <ref> selects the snapshot; see 'spor go --help' for the forms.",
		Example: `  # Bring back a file as it was two snapshots ago
  spor pick @~2 sketch.js

  # Bring back a whole directory from a named snapshot
  spor pick v1.0 shaders/`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			rel, err := projectRelPath(eng.Root(), args[1])
			if err != nil {
				return err
			}

			res, err := eng.Pick(ctx, args[0], rel)
			if err != nil {
				return err
			}
			out := styledOut(cmd)
			if res.Settled {
				fmt.Fprintf(out, "recorded current changes as %s\n", th.Accent.Render(res.SettledID))
			}
			if res.Written == 0 {
				fmt.Fprintln(out, th.Muted.Render(fmt.Sprintf("%s already matches %s; nothing to pick", rel, textfmt.Abbrev(res.Target))))
				return nil
			}
			fmt.Fprintf(out, "picked %s from %s (%s)\n",
				th.Accent.Render(rel), th.Accent.Render(textfmt.Abbrev(res.Target)),
				th.Good.Render(fmt.Sprintf("%d %s", res.Written, textfmt.Plural(res.Written, "file", "files"))))
			if res.Created {
				fmt.Fprintf(out, "recorded as %s\n", th.Accent.Render(res.StateID))
			}
			return nil
		},
	}
}

// projectRelPath converts a user-supplied path (relative to the current
// directory, or absolute) into the slash-separated project-root-relative form
// manifests use, rejecting paths outside the project.
func projectRelPath(root, p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project (%s)", p, root)
	}
	return filepath.ToSlash(rel), nil
}
