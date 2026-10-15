package registry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
)

// FileKeychain reads the credentials of a file in the format of docker's config.json
// ({"auths": {"<registry>": {"auth": "<base64 of user:password>"}}}; the file is the same one that
// kubelet reads on a node). Only this file is consulted: a registry that is not in it gets no credentials.
func FileKeychain(path string) (authn.Keychain, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	var cfg struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			Username      string `json:"username"`
			Password      string `json:"password"`
			IdentityToken string `json:"identitytoken"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse credential file %s: %w", path, err)
	}
	k := fileKeychain{}
	for host, a := range cfg.Auths {
		ac := authn.AuthConfig{Username: a.Username, Password: a.Password, IdentityToken: a.IdentityToken}
		if a.Auth != "" {
			user, pass, ok := decodeAuth(a.Auth)
			if !ok {
				return nil, fmt.Errorf("credential file %s: the auth of %q is not base64 of user:password", path, host)
			}
			ac.Username, ac.Password = user, pass
		}
		k[normalizeHost(host)] = ac
	}
	return k, nil
}

type fileKeychain map[string]authn.AuthConfig

func (k fileKeychain) Resolve(r authn.Resource) (authn.Authenticator, error) {
	if ac, ok := k[normalizeHost(r.RegistryStr())]; ok {
		return authn.FromConfig(ac), nil
	}
	return authn.Anonymous, nil
}

func decodeAuth(s string) (user, pass string, ok bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(b), ":")
	return user, pass, ok
}

// normalizeHost makes the keys of a config.json and a registry name comparable: the scheme and the path
// are dropped, and Docker Hub's names are one.
func normalizeHost(h string) string {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	h, _, _ = strings.Cut(h, "/")
	switch h {
	case "docker.io", "registry-1.docker.io":
		return "index.docker.io"
	}
	return h
}
