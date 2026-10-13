package source_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

func obj(apiVersion, kind, ns, name string, annotated bool, containers ...string) *unstructured.Unstructured {
	cs := []interface{}{}
	for _, img := range containers {
		cs = append(cs, map[string]interface{}{"name": "c", "image": img})
	}
	meta := map[string]interface{}{"namespace": ns, "name": name}
	if annotated {
		meta["annotations"] = map[string]interface{}{source.DefaultAnnotation: "true"}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion, "kind": kind, "metadata": meta,
		"spec": map[string]interface{}{"template": map[string]interface{}{
			"spec": map[string]interface{}{"containers": cs}}},
	}}
}

func TestTargets(t *testing.T) {
	scheme := runtime.NewScheme()
	gvr := func(g, r string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: g, Version: "v1", Resource: r}
	}
	listKinds := map[schema.GroupVersionResource]string{
		gvr("apps", "deployments"):             "DeploymentList",
		gvr("apps", "statefulsets"):            "StatefulSetList",
		gvr("apps", "daemonsets"):              "DaemonSetList",
		gvr("serving.knative.dev", "services"): "ServiceList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds,
		obj("apps/v1", "Deployment", "a", "web", true, "r/app:latest", "r/side@sha256:abc", "r/fixed:v1.2.3", "r/untagged"),
		obj("apps/v1", "Deployment", "a", "skip", false, "r/other:latest"),
		obj("serving.knative.dev/v1", "Service", "b", "ksvc", true, "r/app:latest"),
	)
	got, err := (&source.Kube{Client: client, FloatingTags: []string{"latest"}}).Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []collector.Target{
		{Image: "r/app:latest", Owners: []string{"a/Deployment/web", "b/Service/ksvc"}},
		{Image: "r/untagged", Owners: []string{"a/Deployment/web"}}, // no tag = latest
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestResourcesSaysWhyAnImageIsNotTracked(t *testing.T) {
	dep := obj("apps/v1", "Deployment", "a", "web", true,
		"r/app:latest", "r/app:latest", "r/side@sha256:abc", "r/fixed:v1.2.3")
	none := obj("apps/v1", "Deployment", "a", "plain", true, "r/pinned@sha256:abc")
	got, err := (&source.Kube{Client: fakeClient(dep, none), FloatingTags: []string{"latest"}}).Resources(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	by := map[string]trigger.Resource{got[0].Name: got[0], got[1].Name: got[1]}
	web := by["web"]
	if !reflect.DeepEqual(web.Images, []string{"r/app:latest"}) { // listed once, though two containers use it
		t.Fatalf("images = %v", web.Images)
	}
	reasons := map[string]string{}
	for _, s := range web.Skipped {
		reasons[s.Image] = s.Reason
	}
	if reasons["r/side@sha256:abc"] != "pinned by digest" || !strings.Contains(reasons["r/fixed:v1.2.3"], "not floating") {
		t.Fatalf("skipped = %v", reasons)
	}
	// an annotated resource with nothing to track is still listed
	if p := by["plain"]; len(p.Images) != 0 || len(p.Skipped) != 1 {
		t.Fatalf("%+v", p)
	}
}
