package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// runDocs writes the generated command-line documentation: `pinner docs [dir]` (default docs/cli).
func runDocs(args []string) {
	fs := flag.NewFlagSet("pinner docs", flag.ExitOnError)
	fs.Parse(args)
	dir := "docs/cli"
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	for name, content := range renderCLIDocs() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			fatal(err)
		}
		fmt.Println("wrote", filepath.Join(dir, name))
	}
}
