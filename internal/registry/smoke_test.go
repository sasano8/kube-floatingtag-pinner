package registry_test

import (
	"context"
	"os"
	"testing"

	"github.com/sasano8/kube-floatingtag-pinner/internal/registry"
)

// Network test: run with PINNER_SMOKE_IMAGE=<image> go test ./internal/registry
func TestRemoteDigestSmoke(t *testing.T) {
	img := os.Getenv("PINNER_SMOKE_IMAGE")
	if img == "" {
		t.Skip("PINNER_SMOKE_IMAGE not set")
	}
	d, err := registry.Remote{}.Digest(context.Background(), img)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(d)
}
