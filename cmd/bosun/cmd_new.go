package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/scaffold"
)

func newCmd() *cobra.Command {
	n := &cobra.Command{
		Use:   "new",
		Short: "Scaffold a new Bosun service or project",
	}
	n.AddCommand(newServiceCmd(), newProjectCmd())
	return n
}

func newServiceCmd() *cobra.Command {
	var module, event, bosunDir string
	var port int
	var yes, force bool
	cmd := &cobra.Command{
		Use:   "service <name>",
		Short: "Scaffold a single Bosun service",
		Long: "Scaffolds a service in ./<name>. Inside a Go workspace (a go.work in this " +
			"directory or a parent, such as one made by `bosun new project`), the service is " +
			"created under services/<name> instead, requires the workspace's contracts module, " +
			"and is added to go.work.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ws, err := scaffold.FindWorkspace(".")
			if err != nil {
				return err
			}
			dir, defModule := name, "example.com/"+name
			if ws != nil {
				dir = filepath.Join(ws.Root, "services", name)
				if ws.Module != "" {
					defModule = ws.Module + "/services/" + name
				}
			}
			p := prompter{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout(), yes: yes}
			if module == "" {
				module = p.ask("Go module path", defModule)
			}
			if event == "" {
				event = p.choose("Event backend", []string{"inmem", "amqp", "nats", "redis", "none"}, "inmem")
			}
			local, err := absOrEmpty(bosunDir)
			if err != nil {
				return err
			}
			written, err := scaffold.WriteService(dir, scaffold.Service{
				Module: module, Name: name, Port: port, Event: event, LocalBosun: local,
				Workspace: ws, Force: force,
			})
			if err != nil {
				return forceHint(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %d files in %s\n", len(written), dir)
			tidy(cmd, dir)
			if ws != nil {
				useInWorkspace(cmd, ws, dir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Go module path")
	cmd.Flags().StringVar(&event, "event", "", "event backend: inmem|amqp|nats|redis|none")
	cmd.Flags().IntVar(&port, "port", 8080, "listen port")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept defaults without prompting")
	cmd.Flags().StringVar(&bosunDir, "bosun-dir", "", "build against a local bosun checkout (adds go.mod replace directives)")
	cmd.Flags().BoolVar(&force, "force", false, "write into a non-empty directory, overwriting generated files")
	return cmd
}

func newProjectCmd() *cobra.Command {
	var module, service, event, bosunDir string
	var port int
	var yes, force bool
	cmd := &cobra.Command{
		Use:   "project <name>",
		Short: "Scaffold a workspace with a shared contracts package and a first service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			p := prompter{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout(), yes: yes}
			if module == "" {
				module = p.ask("Go module path", "example.com/"+name)
			}
			if service == "" {
				service = p.ask("First service name", "api")
			}
			if event == "" {
				event = p.choose("Event backend", []string{"inmem", "amqp", "nats", "redis", "none"}, "inmem")
			}
			local, err := absOrEmpty(bosunDir)
			if err != nil {
				return err
			}
			written, err := scaffold.WriteProject(name, scaffold.Project{
				Module: module, Name: name, Service: service, Port: port, Event: event, LocalBosun: local,
				Force: force,
			})
			if err != nil {
				return forceHint(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %d files in %s/\n", len(written), name)
			svcDir := filepath.Join(name, "services", service)
			tidy(cmd, svcDir)
			// Re-sync go.work in case tidy raised the service's go line.
			if root, err := filepath.Abs(name); err == nil {
				useInWorkspace(cmd, &scaffold.Workspace{Root: root}, svcDir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Go module path")
	cmd.Flags().StringVar(&service, "service", "", "name of the first service")
	cmd.Flags().StringVar(&event, "event", "", "event backend: inmem|amqp|nats|redis|none")
	cmd.Flags().IntVar(&port, "port", 8080, "listen port")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept defaults without prompting")
	cmd.Flags().StringVar(&bosunDir, "bosun-dir", "", "build against a local bosun checkout (adds go.mod replace directives)")
	cmd.Flags().BoolVar(&force, "force", false, "write into a non-empty directory, overwriting generated files")
	return cmd
}

func forceHint(err error) error {
	if errors.Is(err, scaffold.ErrExists) {
		return fmt.Errorf("%w (pass --force to overwrite)", err)
	}
	return err
}

// useInWorkspace adds a new service to go.work, or tells the user how to.
func useInWorkspace(cmd *cobra.Command, ws *scaffold.Workspace, dir string) {
	if err := ws.Use(dir); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\nRun `go work use %s` in %s.\n", err, dir, ws.Root)
	}
}

// tidy runs `go mod tidy` in a new service so it builds straight away. A
// failure (no go command, or offline) leaves the files in place and tells the
// user to run it themselves.
func tidy(cmd *cobra.Command, dir string) {
	if err := scaffold.Tidy(dir); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\nRun `go mod tidy` in %s before building.\n", err, dir)
	}
}

func absOrEmpty(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	return filepath.Abs(dir)
}

// prompter reads answers from stdin, or returns defaults when yes is set (CI).
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	yes bool
}

func (p prompter) ask(label, def string) string {
	if p.yes {
		return def
	}
	fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	line, _ := p.in.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

func (p prompter) choose(label string, opts []string, def string) string {
	if p.yes {
		return def
	}
	fmt.Fprintf(p.out, "%s (%s) [%s]: ", label, strings.Join(opts, "/"), def)
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(line)
	for _, o := range opts {
		if o == line {
			return o
		}
	}
	return def
}
