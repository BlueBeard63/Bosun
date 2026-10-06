package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/clientgen"
	"github.com/bluebeard63/bosun/cmd/bosun/internal/registrygen"
	"github.com/bluebeard63/bosun/modules/manifestmod"
)

func genCmd() *cobra.Command {
	gen := &cobra.Command{
		Use:   "gen",
		Short: "Code generators",
	}
	gen.AddCommand(genClientCmd(), genRegistryCmd())
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

func genRegistryCmd() *cobra.Command {
	var dir, out string
	var check bool
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Generate blank imports for every package that registers with Bosun",
		Long: "Scans the current Go module for packages with package-level Bosun registrations " +
			"(var _ = bosun.Controller[...], Service, Middleware, Default, DefaultBind, DefaultDynamic) " +
			"and writes a file that blank-imports them, so main.go needs no hand-maintained side-effect imports. " +
			"Run it from the package that calls bosun.New(), usually via //go:generate bosun gen registry.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := registrygen.FindModuleRoot(dir)
			if err != nil {
				return err
			}
			mod, err := registrygen.Scan(root)
			if err != nil {
				return err
			}
			target, err := registrygen.TargetFor(mod, dir)
			if err != nil {
				return err
			}
			src, err := registrygen.Render(mod, target)
			if err != nil {
				return err
			}
			for _, s := range mod.Skipped {
				if s == target.ImportPath {
					continue // the target itself; it registers by being the binary
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "bosun gen registry: skipping %s: package main can't be imported\n", s)
			}

			outPath := out
			if !filepath.IsAbs(out) {
				outPath = filepath.Join(dir, out)
			}
			if check {
				current, err := os.ReadFile(outPath)
				if err != nil || string(current) != string(src) {
					return fmt.Errorf("%s is out of date; run bosun gen registry (or go generate)", outPath)
				}
				return nil
			}
			if err := os.WriteFile(outPath, src, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "bosun gen registry: wrote %s (%d packages)\n", outPath, countImports(mod, target))
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "package directory to write into (the one that calls bosun.New)")
	cmd.Flags().StringVarP(&out, "out", "o", registrygen.FileName, "generated file name, relative to --dir")
	cmd.Flags().BoolVar(&check, "check", false, "verify the file is up to date instead of writing it (for CI)")
	return cmd
}

func countImports(m *registrygen.Module, target registrygen.Options) int {
	n := 0
	for _, p := range m.Packages {
		if p.ImportPath != target.ImportPath {
			n++
		}
	}
	return n
}
