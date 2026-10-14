package source

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

var (
	podsGVR      = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	revisionsGVR = schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "revisions"}
)

// KubeRunning reads the digests that are actually in use: Pod status.containerStatuses[].imageID
// for workloads, and the latest ready Revision's status.containerStatuses[].imageDigest for
// a Knative Service. It only reads.
type KubeRunning struct {
	Client dynamic.Interface
}

func (k *KubeRunning) Running(ctx context.Context, r trigger.Resource, image string) ([]trigger.Running, error) {
	want, err := normalize(image)
	if err != nil {
		return nil, err
	}
	if r.Kind == "Service" { // Knative Service
		return k.revision(ctx, r, want)
	}
	return k.pods(ctx, r, want)
}

func (k *KubeRunning) pods(ctx context.Context, r trigger.Resource, want string) ([]trigger.Running, error) {
	if r.Selector == "" {
		return nil, fmt.Errorf("%s has no spec.selector.matchLabels to find its Pods", r)
	}
	list, err := k.Client.Resource(podsGVR).Namespace(r.Namespace).List(ctx, metav1.ListOptions{LabelSelector: r.Selector})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	var out []trigger.Running
	for i := range list.Items {
		pod := &list.Items[i]
		if !inUse(pod) {
			continue
		}
		node, _, _ := unstructured.NestedString(pod.Object, "spec", "nodeName")
		css, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
		for _, c := range css {
			m, _ := c.(map[string]interface{})
			img, _ := m["image"].(string)
			if n, err := normalize(img); err != nil || n != want {
				continue
			}
			d := digestOf(m["imageID"])
			slog.Debug("running: pod", "pod", pod.GetName(), "node", node, "image", img, "imageID", m["imageID"], "digest", d)
			if d != "" {
				out = append(out, trigger.Running{Digest: d, Node: node, Pod: pod.GetName(), UID: string(pod.GetUID())})
			}
		}
	}
	return out, nil
}

// inUse says whether a Pod counts as running its image. The definition (docs/design.md, "実行中の Pod"):
// not being deleted (the old Pod while a rollout finishes is still listed, with its old digest), and not
// finished (phase Succeeded / Failed: what a Job leaves behind). Whether the Pod has an image digest to
// compare is decided separately, per container, from imageID.
func inUse(pod *unstructured.Unstructured) bool {
	if pod.GetDeletionTimestamp() != nil {
		return false
	}
	phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase")
	return phase != "Succeeded" && phase != "Failed"
}

func (k *KubeRunning) revision(ctx context.Context, r trigger.Resource, want string) ([]trigger.Running, error) {
	if r.Revision == "" {
		return nil, nil
	}
	rev, err := k.Client.Resource(revisionsGVR).Namespace(r.Namespace).Get(ctx, r.Revision, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get revision %s: %w", r.Revision, err)
	}
	specs, _, _ := unstructured.NestedSlice(rev.Object, "spec", "containers")
	imageByName := map[string]string{}
	for _, c := range specs {
		m, _ := c.(map[string]interface{})
		n, _ := m["name"].(string)
		if n == "" {
			n = "user-container" // Knative's default name
		}
		imageByName[n], _ = m["image"].(string)
	}
	css, _, _ := unstructured.NestedSlice(rev.Object, "status", "containerStatuses")
	var out []trigger.Running
	for _, c := range css {
		m, _ := c.(map[string]interface{})
		cname, _ := m["name"].(string)
		img, ok := imageByName[cname]
		if !ok && len(specs) == 1 {
			for _, v := range imageByName {
				img, ok = v, true
			}
		}
		if !ok {
			continue
		}
		if n, err := normalize(img); err != nil || n != want {
			continue
		}
		d := digestOf(m["imageDigest"])
		slog.Debug("running: revision", "revision", r.Revision, "image", img, "imageDigest", m["imageDigest"], "digest", d)
		if d != "" {
			out = append(out, trigger.Running{Digest: d})
		}
	}
	return out, nil
}

// normalize makes image references comparable (e.g. busybox == docker.io/library/busybox:latest).
func normalize(image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", image, err)
	}
	return ref.Name(), nil
}

// digestOf takes the digest out of "repo@sha256:…". A bare image ID (no "@") has none.
func digestOf(v interface{}) string {
	s, _ := v.(string)
	if _, d, ok := strings.Cut(s, "@"); ok {
		return d
	}
	return ""
}
