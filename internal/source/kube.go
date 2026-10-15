// Package source reads target images from annotated Kubernetes resources.
package source

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

	"github.com/sasano8/pinner/internal/collector"
	"github.com/sasano8/pinner/internal/trigger"
)

// DefaultAnnotation marks a resource as a target (value "true").
const DefaultAnnotation = "floatingtag-pinner/enabled"

type kind struct {
	kind string
	gvr  schema.GroupVersionResource
	// path to the containers' parent (the pod spec)
	podSpec []string
}

var kinds = []kind{
	{"Deployment", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, []string{"spec", "template", "spec"}},
	{"StatefulSet", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, []string{"spec", "template", "spec"}},
	{"DaemonSet", schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, []string{"spec", "template", "spec"}},
	{"Service", schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}, []string{"spec", "template", "spec"}},
}

// Kube lists Deployments, StatefulSets, DaemonSets and Knative Services
// carrying the annotation. It only reads them.
type Kube struct {
	Client     dynamic.Interface
	Namespace  string // "" = all namespaces
	Annotation string // "" = DefaultAnnotation

	// FloatingTags are the tags to track (e.g. "latest"). Images with any other tag
	// are treated as fixed and ignored. nil = track every tag. An image without a
	// tag counts as "latest".
	FloatingTags []string
}

// ResourceFor returns the API resource of a Kind this package reads, so that actions can
// address the same resources. "Service" is the Knative Service here (the core Service is not read).
func ResourceFor(kind string) (schema.GroupVersionResource, bool) {
	for _, kd := range kinds {
		if kd.kind == kind {
			return kd.gvr, true
		}
	}
	return schema.GroupVersionResource{}, false
}

// ActionAnnotationPrefix + <state> names the action for a state, e.g.
// floatingtag-pinner/on-mismatch: restart. Most resources only need on-mismatch.
const ActionAnnotationPrefix = "floatingtag-pinner/on-"

// Resources lists the annotated resources with their floating-tag images.
func (k *Kube) Resources(ctx context.Context) ([]trigger.Resource, error) {
	ann := k.Annotation
	if ann == "" {
		ann = DefaultAnnotation
	}
	var out []trigger.Resource
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
			res := trigger.Resource{
				Kind:      obj.GetKind(),
				Namespace: obj.GetNamespace(),
				Name:      obj.GetName(),
				Selector:  selector(obj),
				Actions:   map[string]string{},
			}
			for _, ev := range trigger.ActedOn {
				if v := obj.GetAnnotations()[ActionAnnotationPrefix+string(ev)]; v != "" {
					res.Actions[string(ev)] = v
				}
			}
			res.Revision, _, _ = unstructured.NestedString(obj.Object, "status", "latestReadyRevisionName")
			seen := map[string]bool{}
			for _, img := range images(obj, kd.podSpec) {
				if seen[img] {
					continue
				}
				seen[img] = true
				if reason := k.skipReason(img); reason != "" {
					res.Skipped = append(res.Skipped, trigger.Skipped{Image: img, Reason: reason})
					continue
				}
				res.Images = append(res.Images, img)
			}
			// An annotated resource is listed even when nothing in it is tracked: that is worth
			// seeing (`pinner collect`). pull and trigger only look at Images.
			out = append(out, res)
		}
	}
	return out, nil
}

// Targets lists the floating-tag images of the annotated resources, de-duplicated,
// with the resources that reference each.
func (k *Kube) Targets(ctx context.Context) ([]collector.Target, error) {
	resources, err := k.Resources(ctx)
	if err != nil {
		return nil, err
	}
	owners := map[string][]string{}
	for _, r := range resources {
		for _, img := range r.Images {
			owners[img] = append(owners[img], r.String())
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

// selector builds the pod label selector from spec.selector.matchLabels.
func selector(obj *unstructured.Unstructured) string {
	m, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels")
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// skipReason says why an image is not tracked ("" = it is).
func (k *Kube) skipReason(image string) string {
	if strings.Contains(image, "@") {
		return "pinned by digest"
	}
	if !k.floating(image) {
		return "tag is not floating (see --floating-tags)"
	}
	return ""
}

func (k *Kube) floating(image string) bool {
	if k.FloatingTags == nil {
		return true
	}
	ref, err := name.ParseReference(image)
	if err != nil {
		return false
	}
	tag, ok := ref.(name.Tag)
	if !ok {
		return false
	}
	for _, t := range k.FloatingTags {
		if t == tag.TagStr() {
			return true
		}
	}
	return false
}

// images returns the images of containers and initContainers.
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
			if img == "" {
				continue
			}
			out = append(out, img)
		}
	}
	return out
}
