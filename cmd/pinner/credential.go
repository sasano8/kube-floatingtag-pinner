package main

import (
	"errors"
	"fmt"
	"os"
)

// credentialEnv names the environment variable that sets --credential.
const credentialEnv = "PINNER_CREDENTIAL"

// resolveCredential decides which credential file to use ("" is none), and where that choice came from.
//
//   - --credential given: that file (an empty value means none).
//   - else PINNER_CREDENTIAL set: that file (set but empty means none, and the default is not looked at).
//   - else none.
//
// A file named by the flag or the variable that does not exist is an error: it is a typo, or a Secret that
// was not mounted. (The container image sets PINNER_CREDENTIAL to a file it holds, with no credentials in
// it; whoever mounts a file over it gives pinner the credentials.)
func resolveCredential(flagValue string, flagSet bool, lookupEnv func(string) (string, bool)) (path, source string, err error) {
	explicit := func(p, src string) (string, string, error) {
		if p == "" {
			return "", src, nil
		}
		if err := mustBeFile(p); err != nil {
			return "", src, fmt.Errorf("%s: %w", src, err)
		}
		return p, src, nil
	}
	if flagSet {
		return explicit(flagValue, "--credential")
	}
	if v, ok := lookupEnv(credentialEnv); ok {
		return explicit(v, credentialEnv)
	}
	return "", "", nil
}

func mustBeFile(p string) error {
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("credential file %q does not exist", p)
	case err != nil:
		return fmt.Errorf("credential file %q: %w", p, err)
	case fi.IsDir():
		return fmt.Errorf("credential file %q is a directory", p)
	}
	return nil
}
