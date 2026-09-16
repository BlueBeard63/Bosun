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

// cliVersion resolves the string reported by `bosun --version`: an explicit
// -ldflags value wins; otherwise the module version stamped in by
// `go install <path>@vX.Y.Z` is used, falling back to "dev" for local
// (`go build`/`go run`) builds that carry no released version.
func cliVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
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
	)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
