// Package source reads target images from annotated Kubernetes resources.
package source

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
)

// DefaultAnnotation marks a resource as a target (value "true").
const DefaultAnnotation = "floatingtag-pinner/enabled"

type kind struct {
	gvr schema.GroupVersionResource
	// path to the containers' parent (the pod spec)
	podSpec []string
}

var kinds = []kind{
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}, []string{"spec", "template", "spec"}},
}

// Kube lists Deployments, StatefulSets, DaemonSets and Knative Services
// carrying the annotation. It only reads them.
type Kube struct {
	Client     dynamic.Interface
	Namespace  string // "" = all namespaces
	Annotation string // "" = DefaultAnnotation
}

func (k *Kube) Targets(ctx context.Context) ([]collector.Target, error) {
	ann := k.Annotation
	if ann == "" {
		ann = DefaultAnnotation
	}
	owners := map[string][]string{}
	for _, kd := range kinds {
		list, err := k.Client.Resource(kd.gvr).Namespace(k.Namespace).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) {
			continue // CRD (e.g. Knative) not installed
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", kd.gvr.Resource, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			if obj.GetAnnotations()[ann] != "true" {
				continue
			}
			owner := fmt.Sprintf("%s/%s/%s", obj.GetNamespace(), obj.GetKind(), obj.GetName())
			for _, img := range images(obj, kd.podSpec) {
				owners[img] = append(owners[img], owner)
			}
		}
	}
	targets := make([]collector.Target, 0, len(owners))
	for img, o := range owners {
		sort.Strings(o)
		targets = append(targets, collector.Target{Image: img, Owners: o})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Image < targets[j].Image })
	return targets, nil
}

// images returns the tag-referenced images of containers and initContainers.
// Images already pinned by digest are skipped.
func images(obj *unstructured.Unstructured, podSpec []string) []string {
	var out []string
	for _, field := range []string{"containers", "initContainers"} {
		cs, _, _ := unstructured.NestedSlice(obj.Object, append(append([]string{}, podSpec...), field)...)
		for _, c := range cs {
			m, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			img, _ := m["image"].(string)
			if img == "" || strings.Contains(img, "@") {
				continue
			}
			out = append(out, img)
		}
	}
	return out
}
