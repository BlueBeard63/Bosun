package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/clientgen"
	"github.com/bluebeard63/bosun/modules/manifestmod"
)

func genCmd() *cobra.Command {
	gen := &cobra.Command{
		Use:   "gen",
		Short: "Code generators",
	}
	gen.AddCommand(genClientCmd())
	return gen
}

func genClientCmd() *cobra.Command {
	var pkg, service, outFile string
	cmd := &cobra.Command{
		Use:   "client <manifest-url-or-file>",
		Short: "Generate a typed Go client from a service's deploy manifest",
		Long: "Reads a service manifest (a URL to a running service, or a JSON file) and " +
			"generates a typed Go client with one method per route. Request and response types " +
			"come from the handler signatures, so pair it with a shared contracts package.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readManifest(args[0])
			if err != nil {
				return err
			}
			var m manifestmod.Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				return fmt.Errorf("decode manifest: %w", err)
			}
			if service == "" {
				service = m.Service
			}
			routes := make([]clientgen.Route, 0, len(m.Routes))
			for _, r := range m.Routes {
				routes = append(routes, clientgen.Route{
					Method: r.Method, Path: r.Path, Operation: r.Operation,
					In: r.In, Out: r.Out, InImport: r.InImport, OutImport: r.OutImport,
				})
			}
			src, err := clientgen.Generate(pkg, service, routes)
			if err != nil {
				return err
			}
			if outFile == "" {
				fmt.Print(src)
				return nil
			}
			return os.WriteFile(outFile, []byte(src), 0o644)
		},
	}
	cmd.Flags().StringVar(&pkg, "package", "client", "package name for the generated file")
	cmd.Flags().StringVar(&service, "service", "", "service name (default: from the manifest)")
	cmd.Flags().StringVarP(&outFile, "out", "o", "", "output file (default: stdout)")
	return cmd
}

// readManifest reads a manifest from a URL or a local file. A URL that is not
// already the manifest path gets /.bosun/manifest appended.
func readManifest(src string) ([]byte, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		url := src
		if !strings.Contains(url, "/.bosun/manifest") {
			url = strings.TrimRight(url, "/") + "/.bosun/manifest"
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", url, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", url, resp.Status)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(src)
}
