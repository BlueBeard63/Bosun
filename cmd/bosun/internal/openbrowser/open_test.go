package openbrowser

import (
	"slices"
	"testing"
)

func TestCommand(t *testing.T) {
	const url = "http://localhost:8080/docs?q=a&b=c"
	for _, tc := range []struct {
		goos string
		name string
		args []string
	}{
		{"darwin", "open", []string{url}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", url}},
		{"linux", "xdg-open", []string{url}},
		{"freebsd", "xdg-open", []string{url}},
		{"openbsd", "xdg-open", []string{url}},
	} {
		name, args := command(tc.goos, url)
		if name != tc.name || !slices.Equal(args, tc.args) {
			t.Errorf("command(%q) = %s %q, want %s %q", tc.goos, name, args, tc.name, tc.args)
		}
	}
}

// The URL must reach the launcher as one argument, never through a shell, so
// characters a shell would interpret stay inert.
func TestCommandPassesURLAsOneArgument(t *testing.T) {
	const url = "http://x/?a=1&b=$(touch pwned);c"
	for _, goos := range []string{"darwin", "windows", "linux"} {
		_, args := command(goos, url)
		if args[len(args)-1] != url {
			t.Errorf("%s: last argument = %q, want the URL unchanged", goos, args[len(args)-1])
		}
	}
}
