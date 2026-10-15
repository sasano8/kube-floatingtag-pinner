package action_test

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/sasano8/pinner/internal/action"
	"github.com/sasano8/pinner/internal/trigger"
)

func pod(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{"namespace": "ns", "name": name},
	}}
}

func finding(ms ...trigger.Mismatch) trigger.Finding {
	return trigger.Finding{
		Resource:   trigger.Resource{Kind: "Deployment", Namespace: "ns", Name: "web"},
		Status:     trigger.StatusMismatch,
		Mismatches: ms,
	}
}

func TestDeletePodsDeletesOnlyTheDifferingPodsByUID(t *testing.T) {
	cl := client(pod("web-1"), pod("web-2"), pod("web-3"))
	type call struct{ name, uid string }
	var calls []call
	cl.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		d := a.(k8stesting.DeleteActionImpl)
		c := call{name: d.GetName()}
		if p := d.GetDeleteOptions().Preconditions; p != nil && p.UID != nil {
			c.uid = string(*p.UID)
		}
		calls = append(calls, c)
		return false, nil, nil
	})

	err := action.DeletePods{Client: cl}.Do(context.Background(), finding(
		trigger.Mismatch{Running: trigger.Running{Pod: "web-1", UID: "uid-1", Digest: "sha256:A"}},
		trigger.Mismatch{Running: trigger.Running{Pod: "web-2", UID: "uid-2", Digest: "sha256:A"}},
		trigger.Mismatch{Running: trigger.Running{}}, // a Revision has no Pod: nothing to delete
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != (call{"web-1", "uid-1"}) || calls[1] != (call{"web-2", "uid-2"}) {
		t.Fatalf("calls = %+v", calls)
	}
	got, _ := cl.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("ns").
		Get(context.Background(), "web-3", metav1.GetOptions{})
	if got == nil {
		t.Fatal("web-3 was not named and must stay")
	}
}

func TestDeletePodsTreatsGoneAndReplacedAsDone(t *testing.T) {
	cl := client()
	cl.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		gr := schema.GroupResource{Resource: "pods"}
		switch a.(k8stesting.DeleteActionImpl).GetName() {
		case "gone":
			return true, nil, apierrors.NewNotFound(gr, "gone")
		case "replaced":
			return true, nil, apierrors.NewConflict(gr, "replaced", errors.New("UID in precondition does not match"))
		case "boom":
			return true, nil, apierrors.NewForbidden(gr, "boom", errors.New("no"))
		}
		return false, nil, nil
	})
	d := action.DeletePods{Client: cl}

	if err := d.Do(context.Background(), finding(
		trigger.Mismatch{Running: trigger.Running{Pod: "gone"}},
		trigger.Mismatch{Running: trigger.Running{Pod: "replaced", UID: "old"}},
	)); err != nil {
		t.Fatalf("a Pod that is gone or already replaced is the goal, not an error: %v", err)
	}
	if err := d.Do(context.Background(), finding(trigger.Mismatch{Running: trigger.Running{Pod: "boom"}})); err == nil {
		t.Fatal("a real failure must be reported")
	}
}

func TestDeletePodsOnlyForWorkloadsAndOnlyOnMismatch(t *testing.T) {
	d := action.DeletePods{Client: client()}
	for kind, want := range map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "Service": false, "Pod": false} {
		if d.Supports(kind) != want {
			t.Errorf("Supports(%s) = %v", kind, !want)
		}
	}
	a := trigger.NewActor(d)
	f := finding(trigger.Mismatch{Running: trigger.Running{Pod: "web-1"}})
	f.Status = trigger.StatusNotPulled
	f.Actions = map[string]string{"not-pulled": "delete-pods"}
	if err := a.Act(context.Background(), f); err == nil {
		t.Fatal("delete-pods names Pods from a mismatch, so it cannot be bound to not-pulled")
	}
	f.Resource.Kind = "Service"
	f.Status, f.Actions = trigger.StatusMismatch, map[string]string{"mismatch": "delete-pods"}
	if err := a.Act(context.Background(), f); err == nil {
		t.Fatal("Knative is not supported")
	}
}
