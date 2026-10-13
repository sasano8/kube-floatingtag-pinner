package action_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/sasano8/kube-floatingtag-pinner/internal/action"
	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

func workload(apiVersion, kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]interface{}{"namespace": "ns", "name": name},
		"spec": map[string]interface{}{"template": map[string]interface{}{
			"metadata": map[string]interface{}{"annotations": map[string]interface{}{"keep": "me"}},
			"spec": map[string]interface{}{"containers": []interface{}{
				map[string]interface{}{"name": "c", "image": "r/app:latest"}}},
		}},
	}}
}

func client(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "apps", Version: "v1", Resource: "deployments"}:             "DeploymentList",
			{Group: "apps", Version: "v1", Resource: "statefulsets"}:            "StatefulSetList",
			{Group: "apps", Version: "v1", Resource: "daemonsets"}:              "DaemonSetList",
			{Group: "serving.knative.dev", Version: "v1", Resource: "services"}: "ServiceList",
		}, objs...)
}

func TestRolloutPatchesOnlyAnAnnotationOnThePodTemplate(t *testing.T) {
	cases := []struct {
		kind string
		obj  *unstructured.Unstructured
	}{
		{"Deployment", workload("apps/v1", "Deployment", "app")},
		{"StatefulSet", workload("apps/v1", "StatefulSet", "app")},
		{"DaemonSet", workload("apps/v1", "DaemonSet", "app")},
		{"Service", workload("serving.knative.dev/v1", "Service", "app")}, // Knative
	}
	for _, c := range cases {
		cl := client(c.obj)
		gvr, _ := source.ResourceFor(c.kind)
		r := action.Rollout{Client: cl, Now: func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) }}
		f := trigger.Finding{Resource: trigger.Resource{Kind: c.kind, Namespace: "ns", Name: "app"}}

		if !r.Supports(c.kind) {
			t.Fatalf("%s must be supported", c.kind)
		}
		if err := r.Do(context.Background(), f); err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		got, err := cl.Resource(gvr).Namespace("ns").Get(context.Background(), "app", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ann, _, _ := unstructured.NestedStringMap(got.Object, "spec", "template", "metadata", "annotations")
		if ann[action.RolloutAnnotation] != "2026-10-13T12:00:00Z" || ann["keep"] != "me" {
			t.Fatalf("%s: annotations = %v", c.kind, ann)
		}
		cs, _, _ := unstructured.NestedSlice(got.Object, "spec", "template", "spec", "containers")
		if img := cs[0].(map[string]interface{})["image"]; img != "r/app:latest" {
			t.Fatalf("%s: the image must not be touched: %v", c.kind, img)
		}
	}
}

func TestRolloutRejectsUnknownKinds(t *testing.T) {
	r := action.Rollout{Client: client()}
	if r.Supports("Pod") {
		t.Fatal("Pod is not a workload this action rolls out")
	}
	if err := r.Do(context.Background(), trigger.Finding{Resource: trigger.Resource{Kind: "Pod", Name: "p"}}); err == nil {
		t.Fatal("must fail")
	}
}
