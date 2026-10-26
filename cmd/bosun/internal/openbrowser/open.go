// Package openbrowser opens a URL in the OS default browser, cross-platform,
// with no external dependency.
package openbrowser

import (
	"os/exec"
	"runtime"
)

// Open launches the default browser pointing at url. It returns as soon as the
// launcher process starts.
func Open(url string) error {
	name, args := command(runtime.GOOS, url)
	return exec.Command(name, args...).Start()
}

// command returns the launcher and its arguments for opening url on goos.
func command(goos, url string) (name string, args []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}
