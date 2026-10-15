// Package action holds the actions that change something in the cluster. Detection and the
// choice of action live in internal/trigger; what an action does to which kind of resource
// lives here.
package action

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/sasano8/pinner/internal/source"
	"github.com/sasano8/pinner/internal/trigger"
)

const (
	// FieldManager names this tool in managedFields, so that e.g. Argo CD can be told to ignore
	// what it changes (ignoreDifferences with managedFieldsManagers).
	FieldManager = "floatingtag-pinner"
	// RolloutAnnotation is put on the pod template. Its value changes on every rollout.
	RolloutAnnotation = "floatingtag-pinner/restartedAt"
)

// Rollout starts a new generation by changing the pod template: one annotation, nothing else.
// The image field is not touched.
//
//   - Deployment, StatefulSet, DaemonSet: the template change makes a rolling update, which
//     starts Pods that pull the tag again.
//   - Knative Service: the template change makes a new Revision, and the tag is resolved to
//     a digest again when the Revision is created. (Deleting Pods would not do: a Revision's Pods
//     are pinned to the digest it resolved. Deleting the Revision is not an option.)
//
// Both kinds have spec.template.metadata.annotations, so the patch is the same.
type Rollout struct {
	Client dynamic.Interface
	Now    func() time.Time
}

func (Rollout) Name() string { return "rollout" }

func (Rollout) Supports(kind string) bool {
	_, ok := source.ResourceFor(kind)
	return ok
}

func (r Rollout) Do(ctx context.Context, f trigger.Finding) error {
	gvr, ok := source.ResourceFor(f.Kind)
	if !ok {
		return fmt.Errorf("rollout does not support %s", f.Kind)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	// Second granularity: two findings of one resource in the same pass (e.g. two images)
	// write the same value, and the second patch changes nothing, so it is one rollout.
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{
			RolloutAnnotation: now().UTC().Format(time.RFC3339),
		}},
	}}})
	if err != nil {
		return err
	}
	_, err = r.Client.Resource(gvr).Namespace(f.Namespace).Patch(ctx, f.Name,
		types.MergePatchType, patch, metav1.PatchOptions{FieldManager: FieldManager})
	if err != nil {
		return fmt.Errorf("patch %s: %w", f.Resource, err)
	}
	return nil
}
