package local_test

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sasano8/kube-floatingtag-pinner/internal/local"
)

func node(name string, names ...[]string) *unstructured.Unstructured {
	imgs := []interface{}{}
	for _, ns := range names {
		l := []interface{}{}
		for _, n := range ns {
			l = append(l, n)
		}
		imgs = append(imgs, map[string]interface{}{"names": l})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]interface{}{"name": name},
		"status":   map[string]interface{}{"images": imgs},
	}}
}

func TestTagDigests(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "NodeList"},
		node("n1",
			[]string{"ghcr.io/o/app@sha256:aaa", "ghcr.io/o/app:latest"},
			[]string{"ghcr.io/o/other@sha256:zzz", "ghcr.io/o/other:latest"}),
		node("n2",
			// pulled by digest only (e.g. by Knative): the tag is not here
			[]string{"ghcr.io/o/app@sha256:bbb"},
			[]string{"docker.io/library/busybox@sha256:ccc", "docker.io/library/busybox:latest"}),
		node("n3", []string{"ghcr.io/o/app@sha256:ddd", "ghcr.io/o/app:v1"}),
	)
	n := &local.Node{Client: client}

	got, err := n.TagDigests(context.Background(), "ghcr.io/o/app:latest")
	if err != nil || !reflect.DeepEqual(got, []string{"sha256:aaa"}) {
		t.Fatalf("got %v err %v (digest-only and other-tag entries must be ignored)", got, err)
	}
	// docker hub names are normalized, so "busybox" matches "docker.io/library/busybox"
	got, _ = n.TagDigests(context.Background(), "busybox")
	if !reflect.DeepEqual(got, []string{"sha256:ccc"}) {
		t.Fatalf("got %v", got)
	}
	got, _ = n.TagDigests(context.Background(), "ghcr.io/o/app:nothere")
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func readyNode(name string) *unstructured.Unstructured {
	n := node(name)
	n.Object["status"].(map[string]interface{})["conditions"] = []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"}}
	return n
}

func TestPull(t *testing.T) {
	gvr := func(r string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Version: "v1", Resource: r}
	}
	notReady := node("down")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr("nodes"): "NodeList", gvr("pods"): "PodList"},
		readyNode("n1"), readyNode("n2"), notReady)

	// the "kubelet": a created pod comes back with its image pulled
	var created []string
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		u := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		created = append(created, u.Object["spec"].(map[string]interface{})["nodeName"].(string))
		c := u.Object["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})
		if c["image"] != "ghcr.io/o/app:latest" {
			t.Errorf("pull image = %v, want the tag reference", c["image"])
		}
		u.Object["status"] = map[string]interface{}{"containerStatuses": []interface{}{
			map[string]interface{}{"imageID": "ghcr.io/o/app@sha256:bbb"}}}
		return false, nil, nil
	})

	n := &local.Node{Client: client, PullNamespace: "pull", PullTimeout: 5 * time.Second}
	pulled, err := n.Pull(context.Background(), "ghcr.io/o/app:latest", "sha256:bbb")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pulled, []string{"sha256:bbb"}) { // what the pull Pods' imageID reported: one value for both nodes
		t.Fatalf("pulled = %v", pulled)
	}
	sort.Strings(created)
	if !reflect.DeepEqual(created, []string{"n1", "n2"}) {
		t.Fatalf("pods created on %v (NotReady nodes must be skipped)", created)
	}
	left, _ := client.Resource(gvr("pods")).Namespace("pull").List(context.Background(), metav1.ListOptions{})
	if len(left.Items) != 0 {
		t.Fatalf("pull pods must be deleted, left %d", len(left.Items))
	}
}

func TestPullReportsImagePullError(t *testing.T) {
	gvr := func(r string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Version: "v1", Resource: r}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr("nodes"): "NodeList", gvr("pods"): "PodList"},
		readyNode("n1"))
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		u := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		u.Object["status"] = map[string]interface{}{"containerStatuses": []interface{}{
			map[string]interface{}{"state": map[string]interface{}{"waiting": map[string]interface{}{
				"reason": "ErrImagePull", "message": "unauthorized"}}}}}
		return false, nil, nil
	})
	n := &local.Node{Client: client, PullTimeout: 5 * time.Second}
	_, err := n.Pull(context.Background(), "ghcr.io/o/app:latest", "sha256:bbb")
	if err == nil || !strings.Contains(err.Error(), "ErrImagePull") {
		t.Fatalf("err = %v", err)
	}
}

func TestNodeTagDigestsPerNode(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "NodeList"},
		node("n1", []string{"ghcr.io/o/app@sha256:aaa", "ghcr.io/o/app:latest"}),
		node("n2", []string{"ghcr.io/o/app@sha256:bbb", "ghcr.io/o/app:latest"}),
		node("n3", []string{"ghcr.io/o/app@sha256:ccc"}), // digest only: not reported
	)
	got, err := (&local.Node{Client: client}).NodeTagDigests(context.Background(), "ghcr.io/o/app:latest")
	want := map[string][]string{"n1": {"sha256:aaa"}, "n2": {"sha256:bbb"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v err %v", got, err)
	}
}
