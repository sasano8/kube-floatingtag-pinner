package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestResolveCredential(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	os.WriteFile(file, []byte(`{}`), 0o600)
	other := filepath.Join(dir, "other.json")
	os.WriteFile(other, []byte(`{}`), 0o600)
	missing := filepath.Join(dir, "missing.json")

	cases := []struct {
		name       string
		flagValue  string
		flagSet    bool
		env        map[string]string
		want       string
		wantErrHas string
	}{
		{name: "nothing: none"},
		{name: "flag wins over the variable", flagValue: file, flagSet: true, env: map[string]string{credentialEnv: other}, want: file},
		{name: "variable alone", env: map[string]string{credentialEnv: other}, want: other},
		{name: "variable set but empty: none", env: map[string]string{credentialEnv: ""}},
		{name: "flag empty: none, whatever the variable says", env: map[string]string{credentialEnv: file}, flagSet: true},
		{name: "flag names a missing file: error (a typo)", flagValue: missing, flagSet: true, wantErrHas: "does not exist"},
		{name: "variable names a missing file: error (a Secret not mounted)", env: map[string]string{credentialEnv: missing}, wantErrHas: credentialEnv},
		{name: "a directory is an error", flagValue: dir, flagSet: true, wantErrHas: "is a directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := resolveCredential(c.flagValue, c.flagSet, env(c.env))
			if c.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrHas) {
					t.Fatalf("err = %v, want one containing %q", err, c.wantErrHas)
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
