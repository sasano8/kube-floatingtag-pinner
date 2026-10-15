package main

import (
	"os"
	"strings"
)

// serviceAccountNamespaceFile holds the namespace a Pod runs in (mounted with its ServiceAccount).
const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// pullNamespace is the namespace for the pull Pods: the one that was asked for, else the namespace pinner
// itself runs in (so that the permissions of its own namespace are the ones that count), else "default".
func pullNamespace(asked string) string {
	return chooseNamespace(asked, serviceAccountNamespaceFile)
}

func chooseNamespace(asked, ownFile string) string {
	if asked != "" {
		return asked
	}
	if b, err := os.ReadFile(ownFile); err == nil {
		if ns := strings.TrimSpace(string(b)); ns != "" {
			return ns
		}
	}
	return "default"
}
