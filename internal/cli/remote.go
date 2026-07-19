package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newRemoteCmd builds `spor remote`, which configures the optional sync server
// (docs/design-spec.md §7). Bare, it reports the current setting.
//
// There is no `remote drop`: sync propagates deletions, so removing history from
// the server is just deleting it here and pushing.
func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Configure the sync server",
		Long: "Show or configure the server this project syncs with. Sync is optional, " +
			"single-user backup: it moves your history between your own machines and keeps " +
			"a copy on a server. It is not collaboration.\n\n" +
			"Run bare to see the current setting.",
		Example: `  # Show the configured server
  spor remote

  # Point this project at a server (mints a project id)
  spor remote add https://spor.example.com --token "$TOKEN"

  # Set up a second machine against the same project
  spor remote add https://spor.example.com --project 01J2X... --token "$TOKEN"

  # Stop syncing (history is left alone, here and on the server)
  spor remote forget`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			eng, err := openHere(ctx)
			if err != nil {
				return err
			}
			defer eng.Close()

			out := styledOut(cmd)
			info, err := eng.Remote(ctx)
			if errors.Is(err, core.ErrNoRemote) {
				fmt.Fprintln(out, th.Muted.Render("No server configured; run 'spor remote add <url>' to set one."))
				return nil
			}
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "Syncing with %s\n", th.Accent.Render(info.URL))
			fmt.Fprintf(out, "  project %s\n", th.Accent.Render(info.ProjectID))
			if info.SyncedAt.IsZero() {
				fmt.Fprintln(out, th.Muted.Render("  never synced; run 'spor push' to send your history"))
			} else {
				fmt.Fprintf(out, "  %s\n", th.Muted.Render(fmt.Sprintf(
					"last synced %s (server generation %d)",
					textfmt.HumanizeSince(info.SyncedAt), info.SyncedGen)))
			}
			if !info.HasToken {
				fmt.Fprintln(out, th.Muted.Render("  no token stored; set one with --token or $"+core.TokenEnvVar))
			}
			return nil
		},
	}
	cmd.AddCommand(newRemoteAddCmd(), newRemoteForgetCmd())
	return cmd
}

// newRemoteAddCmd builds `spor remote add`.
func newRemoteAddCmd() *cobra.Command {
	var project, token string
	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Point this project at a sync server",
		Long: "Configure the server this project syncs with.\n\n" +
			"The first machine mints a project id, which names this project on the " +
			"server; 'spor remote' prints it. Pass that id with --project when setting up " +
			"another machine, so both sync the same history.\n\n" +
			"The token is stored in your user config directory, never in the project, so " +
			"no secret lands in a folder you might share or put in cloud storage. " +
			"$" + core.TokenEnvVar + " overrides it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			eng, err := openOrInitHere(ctx)
			if err != nil {
				return err
			}
			defer eng.Close()

			info, err := eng.RemoteAdd(ctx, args[0], project, token)
			if err != nil {
				return err
			}

			out := styledOut(cmd)
			fmt.Fprintf(out, "Syncing with %s\n", th.Accent.Render(info.URL))
			fmt.Fprintf(out, "  project %s\n", th.Accent.Render(info.ProjectID))
			if project == "" {
				fmt.Fprintln(out, th.Muted.Render(
					"  use that project id with --project to sync another machine to this history"))
			}
			fmt.Fprintln(out, th.Muted.Render("  run 'spor push' to send your history"))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project id to join (omit to mint one)")
	cmd.Flags().StringVar(&token, "token", "", "auth token for the server")
	return cmd
}

// newRemoteForgetCmd builds `spor remote forget`. It matches the vocabulary of
// `spor forget`: it removes the configuration, never the history.
func newRemoteForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Stop syncing with the server",
		Long: "Forget the sync configuration. Your history is left untouched, both here " +
			"and on the server; only the link between them goes.\n\n" +
			"To remove history from the server, delete it here and push: deletions " +
			"propagate.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			eng, err := openHere(ctx)
			if err != nil {
				return err
			}
			defer eng.Close()

			out := styledOut(cmd)
			if _, err := eng.Remote(ctx); errors.Is(err, core.ErrNoRemote) {
				fmt.Fprintln(out, th.Muted.Render("No server configured; nothing to forget."))
				return nil
			}
			if err := eng.RemoteForget(ctx); err != nil {
				return err
			}
			fmt.Fprintln(out, "Stopped syncing; your history is untouched.")
			return nil
		},
	}
}

// openHere opens the store for the current directory, for sync commands that
// need history to already exist.
func openHere(ctx context.Context) (*core.Engine, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return core.OpenExisting(ctx, root)
}

// openOrInitHere is the same but creates the store when there is none, for the
// two commands that set up a second machine: `remote add` and `pull` are run in
// an empty directory precisely so that pull can fill it. The implicit-init guard
// still refuses the filesystem root and the home directory.
func openOrInitHere(ctx context.Context) (*core.Engine, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return core.OpenOrInit(ctx, root)
}
