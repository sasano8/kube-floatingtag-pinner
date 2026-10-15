package main

import (
	"errors"
	"fmt"
	"os"
)

// resolveCredential checks the credential file that --credential (or PINNER_CREDENTIAL, which sets the same
// flag) names, and returns it ("" is none). source says where the value came from, for the error message.
//
// A file that does not exist is an error: it is a typo, or a Secret that was not mounted. An empty value
// means no credentials. (The container image sets PINNER_CREDENTIAL to a file it holds, with no
// credentials in it; whoever mounts a file over it gives pinner the credentials.)
func resolveCredential(value, source string) (string, error) {
	if value == "" {
		return "", nil
	}
	if err := mustBeFile(value); err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return value, nil
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
