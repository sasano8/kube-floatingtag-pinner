package source_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

func pod(name, node string, labels map[string]interface{}, image, imageID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{"namespace": "ns", "name": name, "uid": "uid-" + name, "labels": labels},
		"spec":     map[string]interface{}{"nodeName": node},
		"status": map[string]interface{}{"containerStatuses": []interface{}{
			map[string]interface{}{"name": "c", "image": image, "imageID": imageID}}},
	}}
}

func fakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	gvr := func(g, r string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: g, Version: "v1", Resource: r}
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr("", "pods"):                         "PodList",
		gvr("apps", "deployments"):              "DeploymentList",
		gvr("apps", "statefulsets"):             "StatefulSetList",
		gvr("apps", "daemonsets"):               "DaemonSetList",
		gvr("serving.knative.dev", "services"):  "ServiceList",
		gvr("serving.knative.dev", "revisions"): "RevisionList",
	}, objs...)
}

func TestRunningPods(t *testing.T) {
	web := map[string]interface{}{"app": "web"}
	terminating := pod("web-5", "n1", web, "ghcr.io/o/app:latest", "ghcr.io/o/app@sha256:old") // being deleted: skipped
	terminating.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
	client := fakeClient(
		pod("web-1", "n1", web, "ghcr.io/o/app:latest", "ghcr.io/o/app@sha256:aaa"),
		pod("web-2", "n2", web, "ghcr.io/o/app:latest", "ghcr.io/o/app@sha256:bbb"),
		pod("web-3", "n2", web, "ghcr.io/o/app:latest", "sha256:onlyanid"),                // no repo digest: skipped
		pod("web-4", "n2", web, "ghcr.io/o/sidecar:latest", "ghcr.io/o/sidecar@sha256:s"), // other image
		terminating,
		pod("db-1", "n1", map[string]interface{}{"app": "db"}, "ghcr.io/o/app:latest", "ghcr.io/o/app@sha256:zzz"),
	)
	r := trigger.Resource{Kind: "Deployment", Namespace: "ns", Name: "web", Selector: "app=web"}
	got, err := (&source.KubeRunning{Client: client}).Running(context.Background(), r, "ghcr.io/o/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	want := []trigger.Running{
		{Digest: "sha256:aaa", Node: "n1", Pod: "web-1", UID: "uid-web-1"},
		{Digest: "sha256:bbb", Node: "n2", Pod: "web-2", UID: "uid-web-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestRunningRevision(t *testing.T) {
	rev := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "serving.knative.dev/v1", "kind": "Revision",
		"metadata": map[string]interface{}{"namespace": "ns", "name": "k-00002"},
		"spec": map[string]interface{}{"containers": []interface{}{
			map[string]interface{}{"image": "ghcr.io/o/app:latest"}}}, // name omitted: user-container
		"status": map[string]interface{}{"containerStatuses": []interface{}{
			map[string]interface{}{"name": "user-container", "imageDigest": "ghcr.io/o/app@sha256:aaa"}}},
	}}
	client := fakeClient(rev)
	r := trigger.Resource{Kind: "Service", Namespace: "ns", Name: "k", Revision: "k-00002"}
	got, err := (&source.KubeRunning{Client: client}).Running(context.Background(), r, "ghcr.io/o/app:latest")
	if err != nil || !reflect.DeepEqual(got, []trigger.Running{{Digest: "sha256:aaa"}}) {
		t.Fatalf("got %+v err %v", got, err)
	}
	// no ready Revision yet: nothing to compare
	got, err = (&source.KubeRunning{Client: client}).Running(context.Background(),
		trigger.Resource{Kind: "Service", Namespace: "ns", Name: "k"}, "ghcr.io/o/app:latest")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestResourcesCarryActionSelectorAndRevision(t *testing.T) {
	dep := obj("apps/v1", "Deployment", "a", "web", true, "r/app:latest")
	dep.Object["spec"].(map[string]interface{})["selector"] = map[string]interface{}{
		"matchLabels": map[string]interface{}{"app": "web", "tier": "fe"}}
	dep.SetAnnotations(map[string]string{source.DefaultAnnotation: "true", source.ActionAnnotationPrefix + "mismatch": "none"})
	ksvc := obj("serving.knative.dev/v1", "Service", "b", "k", true, "r/app:latest")
	ksvc.Object["status"] = map[string]interface{}{"latestReadyRevisionName": "k-00003"}

	got, err := (&source.Kube{Client: fakeClient(dep, ksvc), FloatingTags: []string{"latest"}}).Resources(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	byName := map[string]trigger.Resource{got[0].Name: got[0], got[1].Name: got[1]}
	if w := byName["web"]; w.Actions["mismatch"] != "none" || w.Selector != "app=web,tier=fe" {
		t.Fatalf("%+v", w)
	}
	if k := byName["k"]; k.Revision != "k-00003" || len(k.Actions) != 0 {
		t.Fatalf("%+v", k)
	}
}
