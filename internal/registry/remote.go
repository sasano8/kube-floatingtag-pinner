// Package registry resolves image references on a remote registry.
package registry

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Remote asks the registry for the digest only; the image is not downloaded.
// Credentials come from the default keychain (e.g. ~/.docker/config.json).
type Remote struct{}

func (Remote) Digest(ctx context.Context, image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", image, err)
	}
	d, err := remote.Head(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return "", err
	}
	return d.Digest.String(), nil
}
