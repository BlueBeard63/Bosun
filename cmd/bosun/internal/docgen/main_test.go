package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunRemovesStalePages regenerates after deleting a doc and an asset and
// checks their output is gone, so the CI "docs regenerated" check sees it.
func TestRunRemovesStalePages(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	for rel, body := range map[string]string{
		"README.md":      "# Home\n",
		"old-page.md":    "# Old page\n",
		"assets/old.svg": "<svg/>",
	} {
		p := filepath.Join(in, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(out, "keep.txt") // not docgen output: left alone
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(in, out); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"index.md", "old-page.md", "index.json", "docassets/old.svg"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Fatalf("first run did not write %s: %v", rel, err)
		}
	}

	if err := os.Remove(filepath.Join(in, "old-page.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(in, "assets")); err != nil {
		t.Fatal(err)
	}
	if err := run(in, out); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"old-page.md", "docassets"} {
		if _, err := os.Stat(filepath.Join(out, rel)); !os.IsNotExist(err) {
			t.Errorf("%s survived regeneration (err = %v)", rel, err)
		}
	}
	for _, p := range []string{filepath.Join(out, "index.md"), filepath.Join(out, "index.json"), keep} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s missing after regeneration: %v", p, err)
		}
	}
}
