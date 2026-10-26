// Command bosun is the Bosun framework CLI. It serves the documentation as a
// searchable website (bosun docs) and to AI clients over MCP (bosun mcp), and
// hosts the scaffolding, manifest, and codegen subcommands.
package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// version is overridable at build time with -ldflags "-X main.version=vX.Y.Z".
// When unset (the default for `go install`), cliVersion falls back to the
// module version embedded in the binary's build info.
var version = "dev"

// readBuildInfo is debug.ReadBuildInfo, swappable in tests.
var readBuildInfo = debug.ReadBuildInfo

// cliVersion resolves the version every command reports (`bosun --version`,
// the MCP serverInfo, the docs site header): an explicit -ldflags value wins;
// otherwise the module version stamped in by `go install <path>@vX.Y.Z` is
// used, falling back to "dev" for local (`go build`/`go run`) builds that
// carry no released version. Never read the version variable directly.
func cliVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := readBuildInfo(); ok {
		if v := bi.Main.Version; strings.HasPrefix(v, "v") && !strings.Contains(v, "devel") {
			return v
		}
	}
	return version
}

func main() {
	root := &cobra.Command{
		Use:           "bosun",
		Short:         "Bosun framework CLI — docs, MCP, scaffolding and tooling",
		Version:       cliVersion(),
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(
		docsCmd(),
		mcpCmd(),
		manifestCmd(),
		genCmd(),
		newCmd(),
		addCmd(),
	)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
