package source_test

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
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
		obj("apps/v1", "Deployment", "a", "web", true, "r/app:latest", "r/side@sha256:abc"),
		obj("apps/v1", "Deployment", "a", "skip", false, "r/other:latest"),
		obj("serving.knative.dev/v1", "Service", "b", "ksvc", true, "r/app:latest"),
	)
	got, err := (&source.Kube{Client: client}).Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []collector.Target{{Image: "r/app:latest", Owners: []string{"a/Deployment/web", "b/Service/ksvc"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
