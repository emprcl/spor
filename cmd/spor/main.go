// Command spor is an automatic, undo-flavored versioning tool for creative
// coding workflows. See docs/SPEC.md for the design.
package main

import (
	"context"
	"os"

	"github.com/charmbracelet/fang"

	"github.com/emprcl/spor/internal/cli"
)

func main() {
	if err := fang.Execute(context.Background(), cli.Root()); err != nil {
		os.Exit(1)
	}
}
