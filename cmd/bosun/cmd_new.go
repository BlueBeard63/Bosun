package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amberstack/bosun/cmd/bosun/internal/scaffold"
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
	var module, event string
	var port int
	var yes bool
	cmd := &cobra.Command{
		Use:   "service <name>",
		Short: "Scaffold a single Bosun service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			p := prompter{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout(), yes: yes}
			if module == "" {
				module = p.ask("Go module path", "example.com/"+name)
			}
			if event == "" {
				event = p.choose("Event backend", []string{"inmem", "amqp", "nats", "redis", "none"}, "inmem")
			}
			written, err := scaffold.WriteService(name, scaffold.Service{Module: module, Name: name, Port: port, Event: event})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %d files in %s/\n", len(written), name)
			return nil
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Go module path")
	cmd.Flags().StringVar(&event, "event", "", "event backend: inmem|amqp|nats|redis|none")
	cmd.Flags().IntVar(&port, "port", 8080, "listen port")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept defaults without prompting")
	return cmd
}

func newProjectCmd() *cobra.Command {
	var module, service, event string
	var port int
	var yes bool
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
			written, err := scaffold.WriteProject(name, scaffold.Project{
				Module: module, Name: name, Service: service, Port: port, Event: event,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %d files in %s/\n", len(written), name)
			return nil
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Go module path")
	cmd.Flags().StringVar(&service, "service", "", "name of the first service")
	cmd.Flags().StringVar(&event, "event", "", "event backend: inmem|amqp|nats|redis|none")
	cmd.Flags().IntVar(&port, "port", 8080, "listen port")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept defaults without prompting")
	return cmd
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
