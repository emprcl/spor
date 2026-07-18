// Command spor is an automatic, undo-flavored versioning tool for creative
// workflows. See docs/design-spec.md for the design.
package main

import (
	"context"
	"os"

	"github.com/charmbracelet/fang"

	"github.com/emprcl/spor/internal/cli"
	"github.com/emprcl/spor/internal/view"
)

// version is set via -ldflags "-X main.version=..." by goreleaser; it stays
// "dev" for a plain `go build` or `go run`.
var version = "dev"

func main() {
	if err := fang.Execute(
		context.Background(),
		cli.Root(),
		fang.WithColorSchemeFunc(view.HelpColorScheme),
		fang.WithVersion(version),
	); err != nil {
		os.Exit(1)
	}
}
