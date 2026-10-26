package main

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
)

// fakeBuild makes the binary look like `go install ...@<mainVersion>` built it.
func fakeBuild(t *testing.T, mainVersion string) {
	t.Helper()
	old := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/bluebeard63/bosun/cmd/bosun", Version: mainVersion}}, true
	}
	t.Cleanup(func() { readBuildInfo = old })
}

func TestCLIVersion(t *testing.T) {
	for _, c := range []struct{ ldflags, main, want string }{
		{"dev", "v0.7.0", "v0.7.0"},
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"},
		{"v9.9.9", "v0.7.0", "v9.9.9"}, // -ldflags -X main.version wins
	} {
		fakeBuild(t, c.main)
		old := version
		version = c.ldflags
		got := cliVersion()
		version = old
		if got != c.want {
			t.Errorf("version=%q main=%q: cliVersion() = %q, want %q", c.ldflags, c.main, got, c.want)
		}
	}
}

// TestMCPReportsModuleVersion checks that `bosun mcp` reports the installed
// module version in serverInfo, not the "dev" default of the version variable.
func TestMCPReportsModuleVersion(t *testing.T) {
	fakeBuild(t, "v0.7.0")
	cmd := mcpCmd()
	var out strings.Builder
	cmd.SetIn(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp); err != nil {
		t.Fatalf("bad response %q: %v", out.String(), err)
	}
	if got := resp.Result.ServerInfo.Version; got != "v0.7.0" {
		t.Fatalf("serverInfo.version = %q, want v0.7.0", got)
	}
}
