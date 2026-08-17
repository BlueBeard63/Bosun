// Command bosun is the Bosun framework CLI. It serves the documentation as a
// searchable website (bosun docs) and to AI clients over MCP (bosun mcp), and
// hosts the scaffolding, manifest, and codegen subcommands.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

// version is overridable at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "bosun",
		Short:         "Bosun framework CLI — docs, MCP, scaffolding and tooling",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(
		docsCmd(),
		mcpCmd(),
		manifestCmd(),
		genCmd(),
	)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
