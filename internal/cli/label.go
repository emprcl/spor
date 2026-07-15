package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newLabelCmd builds `spor label`, which lists labels with no arguments,
// names a state with `<ref> <name>`, and removes a label with `-d <name>`
// (docs/design-spec.md §6). The no-arg listing form mirrors `git tag`.
func newLabelCmd() *cobra.Command {
	var del bool

	cmd := &cobra.Command{
		Use:   "label [<ref> <name>]",
		Short: "Name a snapshot, remove a label, or list existing labels",
		Long: "With no arguments, list every label and the snapshot it names. With a " +
			"<ref> and a <name>, name that snapshot so you can refer to it by name " +
			"anywhere a <ref> is accepted. With -d <name>, remove that label. A label " +
			"is a unique alias, like a snapshot id: naming or unnaming a snapshot " +
			"never changes your history.",
		Example: `  # Name the current state
  spor label @ v1.0

  # Name a snapshot by its id
  spor label 01ARZ7 milestone

  # Remove a label
  spor label -d milestone

  # List all labels
  spor label`,
		Args: func(_ *cobra.Command, args []string) error {
			if del {
				if len(args) != 1 {
					return fmt.Errorf("expected exactly one <name> with -d, got %d", len(args))
				}
				return nil
			}
			if len(args) == 0 || len(args) == 2 {
				return nil
			}
			return fmt.Errorf("expected no arguments (to list) or <ref> <name> (to set), got %d", len(args))
		},
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

			if del {
				res, err := eng.Unlabel(ctx, args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed label %q from %s\n", res.Name, res.StateID)
				return nil
			}

			if len(args) == 0 {
				return runLabelList(ctx, cmd, eng)
			}
			res, err := eng.Label(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "labeled %s as %q\n", res.StateID, res.Name)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&del, "delete", "d", false, "remove a label")
	return cmd
}

// runLabelList prints every label with its (abbreviated) state id and age,
// reusing the log's styles so the two read alike.
func runLabelList(ctx context.Context, cmd *cobra.Command, eng *core.Engine) error {
	labels, err := eng.ListLabels(ctx)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(labels) == 0 {
		fmt.Fprintln(out, "No labels yet. Name a snapshot with 'spor label <ref> <name>'.")
		return nil
	}

	ids := make([]string, len(labels))
	for i, l := range labels {
		ids[i] = l.StateID
	}
	short := shortLen(ids)

	w := colorprofile.NewWriter(out, os.Environ())
	for _, l := range labels {
		id := l.StateID
		if len(id) > short {
			id = id[:short]
		}
		fmt.Fprintln(w, styleLabel.Render(l.Name)+"  "+
			styleID.Render(id)+"  "+styleTime.Render(humanizeSince(l.CreatedAt)))
	}
	return nil
}
