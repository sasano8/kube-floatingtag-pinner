package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated documentation is committed (docs/cli). It must match what the flag definitions say,
// so that adding or changing a flag without regenerating the pages fails here.
func TestCLIDocsAreUpToDate(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "cli")
	const hint = "run: go run ./cmd/pinner docs docs/cli"
	want := renderCLIDocs()
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s is missing (%s): %v", name, hint, err)
		}
		if string(got) != content {
			t.Errorf("docs/cli/%s is out of date (%s)", name, hint)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := want[e.Name()]; !ok {
			t.Errorf("docs/cli/%s is not generated any more; remove it (%s)", e.Name(), hint)
		}
	}
}

func TestDocsListEveryFlagOfEveryCommand(t *testing.T) {
	pages := renderCLIDocs()
	for _, flagName := range []string{"--kubeconfig", "--floating-tags", "--dry-run", "--log-level", "--log-states", "--on-new", "--max-actions", "--output", "-o"} {
		found := false
		for _, p := range pages {
			if strings.Contains(p, "| `"+flagName+"` |") {
				found = true
			}
		}
		if !found {
			t.Errorf("no page documents %s", flagName)
		}
	}
	if _, ok := pages["docs.md"]; ok {
		t.Error("the hidden docs command must not be documented")
	}
	if u := usage(); strings.Contains(u, "docs ") || !strings.Contains(u, "collect") {
		t.Errorf("usage = %q", u)
	}
}
