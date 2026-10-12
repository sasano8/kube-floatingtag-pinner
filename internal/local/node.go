// Package local holds Local implementations (the local registry).
package local

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
)

var nodes = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}

// Node reads the image stores of the cluster's nodes through Node.status.images
// (kubelet reports at most 50 images per node). Images are brought in by the
// kubelet when a Pod needs them, so Pull is not implemented yet.
// Digests are the union over all nodes.
type Node struct {
	Client dynamic.Interface
}

func (n *Node) Digests(ctx context.Context, image string) ([]string, error) {
	repo, err := repoOf(image)
	if err != nil {
		return nil, err
	}
	list, err := n.Client.Resource(nodes).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	set := map[string]struct{}{}
	for i := range list.Items {
		imgs, _, _ := unstructured.NestedSlice(list.Items[i].Object, "status", "images")
		for _, img := range imgs {
			m, _ := img.(map[string]interface{})
			names, _ := m["names"].([]interface{})
			for _, nm := range names {
				s, _ := nm.(string)
				r, d, ok := strings.Cut(s, "@")
				if !ok {
					continue // tag-only name
				}
				if nr, err := repoOf(r); err == nil && nr == repo {
					set[d] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

func (n *Node) Pull(context.Context, string, string) error {
	return collector.ErrPullNotImplemented
}

// repoOf normalizes an image reference to its repository name
// (so docker.io/library/x and x match).
func repoOf(image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", image, err)
	}
	return ref.Context().Name(), nil
}
