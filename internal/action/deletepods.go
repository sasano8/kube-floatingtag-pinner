package action

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// DeletePods deletes the Pods whose digest differs from the local cache; the workload's controller
// (ReplicaSet, StatefulSet, DaemonSet) creates them again, and they start from the local cache. The
// resource itself is not touched at all.
//
// Not for Knative: a Revision's Pods are pinned to the digest it resolved, so they would come back the
// same (rollout makes a new Revision). Only for the state mismatch: that is what names the Pods.
//
// All the differing Pods are deleted at once, so the workload can be briefly short of Pods. rollout
// replaces them gradually, following the rolling-update strategy, if that matters.
type DeletePods struct {
	Client dynamic.Interface
}

func (DeletePods) Name() string { return "delete-pods" }

func (DeletePods) Supports(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		return true
	}
	return false
}

func (DeletePods) States() []trigger.Status { return []trigger.Status{trigger.StatusMismatch} }

func (d DeletePods) Do(ctx context.Context, f trigger.Finding) error {
	var errs []error
	for _, m := range f.Mismatches {
		if m.Pod == "" {
			continue
		}
		opts := metav1.DeleteOptions{}
		if m.UID != "" {
			// Only that very Pod: if it was already replaced, the one that took its place is left alone.
			uid := types.UID(m.UID)
			opts.Preconditions = &metav1.Preconditions{UID: &uid}
		}
		err := d.Client.Resource(podsGVR).Namespace(f.Namespace).Delete(ctx, m.Pod, opts)
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			// not found, or the UID no longer matches: it is gone or already replaced, which is the goal
			errs = append(errs, fmt.Errorf("delete pod %s/%s: %w", f.Namespace, m.Pod, err))
		}
	}
	return errors.Join(errs...)
}
