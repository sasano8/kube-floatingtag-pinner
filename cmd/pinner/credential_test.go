package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCredential(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	os.WriteFile(file, []byte(`{}`), 0o600)
	missing := filepath.Join(dir, "missing.json")

	cases := []struct {
		name       string
		value      string
		want       string
		wantErrHas string
	}{
		{name: "empty: none"},
		{name: "a file that exists", value: file, want: file},
		{name: "a missing file: error (a typo, or a Secret not mounted)", value: missing, wantErrHas: "does not exist"},
		{name: "a directory is an error", value: dir, wantErrHas: "is a directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveCredential(c.value, "PINNER_CREDENTIAL")
			if c.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrHas) || !strings.Contains(err.Error(), "PINNER_CREDENTIAL") {
					t.Fatalf("err = %v, want one naming the source and containing %q", err, c.wantErrHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}
