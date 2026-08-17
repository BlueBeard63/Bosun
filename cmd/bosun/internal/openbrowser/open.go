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
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	return exec.Command(name, args...).Start()
}
