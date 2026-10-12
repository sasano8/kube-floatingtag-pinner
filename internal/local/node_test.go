package local_test

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

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

func TestDigests(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "NodeList"},
		node("n1",
			[]string{"ghcr.io/o/app@sha256:aaa", "ghcr.io/o/app:latest"},
			[]string{"ghcr.io/o/other@sha256:zzz"}),
		node("n2",
			[]string{"ghcr.io/o/app@sha256:bbb"},
			[]string{"docker.io/library/busybox@sha256:ccc"}),
	)
	n := &local.Node{Client: client}

	got, err := n.Digests(context.Background(), "ghcr.io/o/app:latest")
	if err != nil || !reflect.DeepEqual(got, []string{"sha256:aaa", "sha256:bbb"}) {
		t.Fatalf("got %v err %v", got, err)
	}
	// docker hub names are normalized, so "busybox" matches "docker.io/library/busybox"
	got, _ = n.Digests(context.Background(), "busybox:latest")
	if !reflect.DeepEqual(got, []string{"sha256:ccc"}) {
		t.Fatalf("got %v", got)
	}
}
