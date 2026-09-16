package main

import (
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/docsite"
	"github.com/bluebeard63/bosun/cmd/bosun/internal/mcpsrv"
)

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the Bosun documentation to AI clients over MCP (stdio)",
		Long: "Runs a Model Context Protocol server on stdin/stdout that exposes the " +
			"Bosun documentation as resources and search tools. Install it with:\n\n" +
			"  claude mcp add bosun-docs -- bosun mcp",
		RunE: func(cmd *cobra.Command, _ []string) error {
			site, err := docsite.Load()
			if err != nil {
				return err
			}
			// stdout is the protocol channel; never print to it here.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return mcpsrv.Serve(ctx, site, os.Stdin, os.Stdout, version)
		},
	}
}
