package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amberstack/bosun/modules/manifestmod"
)

func manifestCmd() *cobra.Command {
	var caddy bool
	cmd := &cobra.Command{
		Use:   "manifest <service-url>",
		Short: "Fetch a service's deploy manifest, or a Caddyfile derived from it",
		Long: "Fetches GET <service-url>/.bosun/manifest and prints the JSON manifest. " +
			"With --caddy, prints a Caddy reverse-proxy site block instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := strings.TrimRight(args[0], "/") + "/.bosun/manifest"
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get(url)
			if err != nil {
				return fmt.Errorf("fetch %s: %w", url, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s: %s", url, resp.Status)
			}
			if !caddy {
				fmt.Println(strings.TrimRight(string(body), "\n"))
				return nil
			}
			var m manifestmod.Manifest
			if err := json.Unmarshal(body, &m); err != nil {
				return fmt.Errorf("decode manifest: %w", err)
			}
			fmt.Print(manifestmod.Caddyfile(m))
			return nil
		},
	}
	cmd.Flags().BoolVar(&caddy, "caddy", false, "emit a Caddyfile instead of the JSON manifest")
	return cmd
}
