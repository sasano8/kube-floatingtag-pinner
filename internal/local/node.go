// Package local holds Local implementations (the local cache of images).
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	nodes   = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// Node reads the image stores of the cluster's nodes through Node.status.images
// (kubelet reports at most 50 images per node). Digests are the union over all nodes.
//
// Pull makes every Ready node's kubelet pull the image by running a short-lived Pod
// pinned to that node. The Pod's command does not exist, so the container never runs;
// only the pull matters. The Pod is deleted once the image has been pulled.
type Node struct {
	Client dynamic.Interface

	PullNamespace string        // namespace for the pull Pods (default "default")
	PullSecret    string        // optional imagePullSecret name in PullNamespace
	PullTimeout   time.Duration // per node (default 2m)
}

// TagDigests returns the digests the image's tag points to on the nodes that hold
// the tag, as one sorted list. See NodeTagDigests.
func (n *Node) TagDigests(ctx context.Context, image string) ([]string, error) {
	byNode, err := n.NodeTagDigests(ctx, image)
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for _, ds := range byNode {
		for _, d := range ds {
			set[d] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// NodeTagDigests returns, per node that holds the tag, the digests the tag points to.
// The node reports an image under several names (e.g. repo:latest and
// repo@sha256:…); the tag's digest is the one sharing an entry with the tag name.
// Digests held without the tag (e.g. pulled by digest) are ignored.
func (n *Node) NodeTagDigests(ctx context.Context, image string) (map[string][]string, error) {
	parsed, err := name.ParseReference(image)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", image, err)
	}
	tag := parsed.Name()
	repo := parsed.Context().Name()

	list, err := n.Client.Resource(nodes).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	out := map[string][]string{}
	for i := range list.Items {
		node := list.Items[i].GetName()
		set := map[string]struct{}{}
		imgs, _, _ := unstructured.NestedSlice(list.Items[i].Object, "status", "images")
		for _, img := range imgs {
			m, _ := img.(map[string]interface{})
			names, _ := m["names"].([]interface{})
			hasTag := false
			var digests []string
			for _, nm := range names {
				s, _ := nm.(string)
				if r, d, ok := strings.Cut(s, "@"); ok {
					if nr, err := repoOf(r); err == nil && nr == repo {
						digests = append(digests, d)
					}
					continue
				}
				if ref, err := name.ParseReference(s); err == nil && ref.Name() == tag {
					hasTag = true
				}
			}
			if hasTag {
				for _, d := range digests {
					set[d] = struct{}{}
				}
			}
		}
		if len(set) == 0 {
			continue
		}
		ds := make([]string, 0, len(set))
		for d := range set {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		out[node] = ds
	}
	return out, nil
}

// Pull pulls the image by the same reference the workloads use (the tag), so the
// node's tag (e.g. :latest) is moved to the new image and a later Pod with
// IfNotPresent starts from it. digest is the remote digest that triggered the pull;
// it only names the Pod so that a new digest gets a new pull.
func (n *Node) Pull(ctx context.Context, image, digest string) ([]string, error) {
	parsed, err := name.ParseReference(image)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", image, err)
	}
	ref := parsed.Name()

	list, err := n.Client.Resource(nodes).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		errs   []error
		pulled = map[string]struct{}{}
	)
	for i := range list.Items {
		node := &list.Items[i]
		if !ready(node) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := n.pullOn(ctx, node.GetName(), ref, digest)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("node %s: %w", node.GetName(), err))
				return
			}
			if got != "" {
				pulled[got] = struct{}{}
			}
		}()
	}
	wg.Wait()
	out := make([]string, 0, len(pulled))
	for d := range pulled {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, errors.Join(errs...)
}

// pullOn pulls on one node and returns the digest the node reports for what it pulled (the pull Pod's
// imageID). That is known as soon as the pull is done, unlike Node.status.images, which the kubelet
// refreshes only now and then.
func (n *Node) pullOn(ctx context.Context, node, ref, digest string) (string, error) {
	ns := n.PullNamespace
	if ns == "" {
		ns = "default"
	}
	timeout := n.PullTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	sum := sha256.Sum256([]byte(ref + "|" + digest + "|" + node))
	podName := "pinner-pull-" + hex.EncodeToString(sum[:5])

	spec := map[string]interface{}{
		"nodeName":      node,
		"restartPolicy": "Never",
		"containers": []interface{}{map[string]interface{}{
			"name":  "pull",
			"image": ref,
			// Always, not IfNotPresent: the node may already hold this tag at an older digest, and
			// IfNotPresent would take that as "present" and pull nothing. Always asks the registry, and
			// pulls (and moves the tag) when the digest differs.
			"imagePullPolicy": "Always",
			"command":         []interface{}{"/floatingtag-pinner-pull-only"},
		}},
	}
	if n.PullSecret != "" {
		spec["imagePullSecrets"] = []interface{}{map[string]interface{}{"name": n.PullSecret}}
	}
	pod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{
			"name": podName, "namespace": ns,
			"labels":      map[string]interface{}{"app.kubernetes.io/managed-by": "floatingtag-pinner"},
			"annotations": map[string]interface{}{"floatingtag-pinner/image": ref, "floatingtag-pinner/digest": digest, "floatingtag-pinner/node": node},
		},
		"spec": spec,
	}}

	pods := n.Client.Resource(podsGVR).Namespace(ns)
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create pull pod: %w", err)
	}
	defer pods.Delete(context.WithoutCancel(ctx), podName, metav1.DeleteOptions{})

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		got, err := pods.Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get pull pod: %w", err)
		}
		if d, done, err := pullState(got); done || err != nil {
			return d, err
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for pull of %s", ref)
		case <-time.After(2 * time.Second):
		}
	}
}

// pullState inspects the pull Pod: pulled once the container status carries an imageID (its
// digest is returned: "repo@sha256:…" -> "sha256:…"); failed when the kubelet reports an image pull error.
func pullState(pod *unstructured.Unstructured) (digest string, done bool, err error) {
	css, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	for _, c := range css {
		m, _ := c.(map[string]interface{})
		if id, _ := m["imageID"].(string); id != "" {
			_, d, _ := strings.Cut(id, "@") // a bare image ID has no repo digest: "" (still pulled)
			return d, true, nil
		}
		reason, _, _ := unstructured.NestedString(m, "state", "waiting", "reason")
		switch reason {
		case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull":
			msg, _, _ := unstructured.NestedString(m, "state", "waiting", "message")
			return "", false, fmt.Errorf("%s: %s", reason, msg)
		}
	}
	return "", false, nil
}

func ready(node *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(node.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]interface{})
		if m["type"] == "Ready" {
			return m["status"] == "True"
		}
	}
	return false
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
