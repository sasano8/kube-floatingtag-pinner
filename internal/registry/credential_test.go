package registry_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/sasano8/pinner/internal/registry"
)

func resolve(t *testing.T, k authn.Keychain, image string) authn.AuthConfig {
	t.Helper()
	ref, err := name.ParseReference(image)
	if err != nil {
		t.Fatal(err)
	}
	a, err := k.Resolve(ref.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := a.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	return *cfg
}

func TestFileKeychain(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"auths": {
	  "ghcr.io": {"auth": "` + enc + `"},
	  "https://index.docker.io/v1/": {"username": "bob", "password": "pw"},
	  "registry.example.com:5000": {"identitytoken": "tok"}
	}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := registry.FileKeychain(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := resolve(t, k, "ghcr.io/o/app:latest"); got.Username != "alice" || got.Password != "s3cret" {
		t.Errorf("ghcr.io: %+v", got)
	}
	if got := resolve(t, k, "alpine:3.20"); got.Username != "bob" || got.Password != "pw" {
		t.Errorf("docker hub (an alias of index.docker.io): %+v", got)
	}
	if got := resolve(t, k, "registry.example.com:5000/x:1"); got.IdentityToken != "tok" {
		t.Errorf("identity token: %+v", got)
	}
	if got := resolve(t, k, "quay.io/o/app:latest"); got != (authn.AuthConfig{}) {
		t.Errorf("a registry that is not in the file gets no credentials: %+v", got)
	}
}

func TestFileKeychainErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := registry.FileKeychain(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing file must be an error")
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"auths": {"ghcr.io": {"auth": "!!!"}}}`), 0o600)
	if _, err := registry.FileKeychain(bad); err == nil {
		t.Error("an auth that is not base64 of user:password must be an error")
	}
	notJSON := filepath.Join(dir, "x.json")
	os.WriteFile(notJSON, []byte(`not json`), 0o600)
	if _, err := registry.FileKeychain(notJSON); err == nil {
		t.Error("invalid JSON must be an error")
	}
}

// The container image holds a credential file with no credentials in it.
func TestFileKeychainEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"auths":{}}`), 0o600)
	k, err := registry.FileKeychain(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolve(t, k, "ghcr.io/o/app:latest"); got != (authn.AuthConfig{}) {
		t.Errorf("no credentials expected: %+v", got)
	}
}
