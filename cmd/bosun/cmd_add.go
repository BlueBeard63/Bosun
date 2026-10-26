package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/registrygen"
	"github.com/bluebeard63/bosun/cmd/bosun/internal/scaffold"
)

// addCmd generates components inside an existing module. It is a separate
// verb from `bosun new`, whose subcommands each create a whole new codebase:
// `bosun new service` scaffolds a service module, `bosun add service` a
// service type.
func addCmd() *cobra.Command {
	a := &cobra.Command{
		Use:   "add",
		Short: "Add a controller, service or middleware to an existing Bosun module",
		Long: "Generates one file declaring a Bosun controller, service or middleware inside the current " +
			"Go module (the nearest go.mod), then regenerates the module's " + registrygen.FileName +
			" so the package is wired in. Inside a workspace made by `bosun new project`, run it in a " +
			"service's directory or pass --in <service>. To create a new service module, use `bosun new service`.",
	}
	a.AddCommand(
		addComponentCmd(scaffold.KindController, "Add a controller (bosun.Controller[T]) with a typed GET handler",
			"internal/<name>/<name>_controller.go"),
		addComponentCmd(scaffold.KindService, "Add an injectable service type (bosun.Service[T])",
			"internal/<name>/<name>_service.go"),
		addComponentCmd(scaffold.KindMiddleware, "Add middleware (bosun.Middleware[T])",
			"internal/middleware/<name>.go"),
	)
	return a
}

func addComponentCmd(kind, short, defaultFile string) *cobra.Command {
	var dir, pkg, in, prefix string
	var force bool
	cmd := &cobra.Command{
		Use:   kind + " <name>",
		Short: short,
		Long: short + ". The file defaults to " + defaultFile + " under the module root; " +
			"--dir picks another package directory (relative to the module root) and --pkg its package name. " +
			"Existing files are never overwritten without --force.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := scaffold.FindModule(".", in)
			if err != nil {
				return err
			}
			file, err := scaffold.AddComponent(scaffold.Component{
				Kind: kind, Name: args[0], Root: root, Dir: dir, Pkg: pkg, Prefix: prefix, Force: force,
			})
			if errors.Is(err, scaffold.ErrFileExists) {
				return fmt.Errorf("%w (pass --force to overwrite)", err)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", display(file))
			regs, err := scaffold.RegenerateRegistries(root)
			if err != nil {
				return fmt.Errorf("regenerate %s: %w", registrygen.FileName, err)
			}
			for _, r := range regs {
				fmt.Fprintf(cmd.OutOrStdout(), "Updated %s\n", display(r))
			}
			if len(regs) == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: no %s in %s; run `bosun gen registry` in your main package, "+
					"or import the new package yourself.\n", registrygen.FileName, root)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "package directory relative to the module root (default "+filepath.ToSlash(filepath.Dir(defaultFile))+")")
	cmd.Flags().StringVar(&pkg, "pkg", "", "package name (default: the existing package in --dir, else its base name)")
	cmd.Flags().StringVar(&in, "in", "", "module to add to: a directory with a go.mod, or a workspace service name")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the file if it exists")
	if kind == scaffold.KindController {
		cmd.Flags().StringVar(&prefix, "prefix", "", "route prefix (default /<name>)")
	}
	return cmd
}

// display shortens path relative to the working directory when it is inside it.
func display(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	if rel, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
