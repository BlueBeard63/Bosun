package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/amberstack/bosun/cmd/bosun/internal/docsite"
	"github.com/amberstack/bosun/cmd/bosun/internal/openbrowser"
)

func docsCmd() *cobra.Command {
	var addr string
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Serve the searchable Bosun documentation and open it in your browser",
		Long: "Starts a local web server hosting the Bosun documentation — fully " +
			"searchable across page titles, headings and body text — and opens it in " +
			"your default browser. The site is embedded in the binary and works offline.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			site, err := docsite.Load()
			if err != nil {
				return fmt.Errorf("loading docs: %w", err)
			}
			site.Version = version
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", addr, err)
			}
			url := "http://" + ln.Addr().String()
			srv := &http.Server{Handler: site.Handler()}

			go func() { _ = srv.Serve(ln) }()
			fmt.Printf("Bosun docs serving at %s  (press ctrl-c to stop)\n", url)
			if !noOpen {
				if err := openbrowser.Open(url); err != nil {
					fmt.Fprintf(os.Stderr, "could not open browser (%v); visit %s manually\n", err, url)
				}
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			<-ctx.Done()

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			fmt.Println("\nshutting down…")
			return srv.Shutdown(shutdownCtx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "listen address (host:port; :0 picks a free port)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser automatically")
	return cmd
}
