package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChooseNamespace(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "namespace")
	os.WriteFile(own, []byte("pinner-system\n"), 0o600)
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)

	cases := []struct {
		name, asked, file, want string
	}{
		{"asked wins", "mine", own, "mine"},
		{"inside a cluster: the namespace pinner runs in", "", own, "pinner-system"},
		{"outside a cluster: default", "", filepath.Join(dir, "missing"), "default"},
		{"an empty file: default", "", empty, "default"},
	}
	for _, c := range cases {
		if got := chooseNamespace(c.asked, c.file); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
