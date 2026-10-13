// Package trigger is stage 2: with the local cache as the source of truth, find the
// resources that run an older image than the local cache holds, and hand each one to
// the Action its annotation names. Detection is separate from what is done about it.
// See docs/design.md.
package trigger

import (
	"context"
	"fmt"
	"sort"
)

// Resource is an annotated workload and the floating-tag images it references.
type Resource struct {
	Kind      string // Deployment, StatefulSet, DaemonSet, or Service (Knative)
	Namespace string
	Name      string
	Images    []string
	Action    string // name from the action annotation; "" = default
	Selector  string // pod label selector (workloads)
	Revision  string // latest ready Revision (Knative Service)
}

func (r Resource) String() string { return fmt.Sprintf("%s/%s/%s", r.Namespace, r.Kind, r.Name) }

type ResourceSource interface {
	Resources(ctx context.Context) ([]Resource, error)
}

// Running is a digest actually in use: by a Pod (Node set), or by a Knative Revision.
type Running struct {
	Digest string
	Node   string
	Pod    string
}

type RunningReader interface {
	Running(ctx context.Context, r Resource, image string) ([]Running, error)
}

// LocalReader reports, per node, the digests the image's tag points to in the local cache.
type LocalReader interface {
	NodeTagDigests(ctx context.Context, image string) (map[string][]string, error)
}

type Status string

const (
	StatusInSync    Status = "in-sync"
	StatusStale     Status = "stale"      // something runs a digest the local tag no longer points to
	StatusNoLocal   Status = "no-local"   // the local cache does not hold the tag (run pull first)
	StatusNoRunning Status = "no-running" // no Pod / Revision digest to compare (e.g. scaled to zero)
	StatusError     Status = "error"
)

// Staleness is one running digest that differs from what the local cache holds.
type Staleness struct {
	Running
	Want []string
}

type Finding struct {
	Resource
	Image  string
	Status Status
	Local  []string // union over nodes
	Stale  []Staleness
	Err    error
}

type Detector struct {
	Source  ResourceSource
	Local   LocalReader
	Running RunningReader
}

// Detect checks every (resource, image). Failures for one image are reported in its
// finding; only a failure to list the resources aborts.
func (d *Detector) Detect(ctx context.Context) ([]Finding, error) {
	resources, err := d.Source.Resources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	perImage := map[string]map[string][]string{} // one local read per image per pass
	var out []Finding
	for _, res := range resources {
		for _, image := range res.Images {
			byNode, ok := perImage[image]
			var err error
			if !ok {
				byNode, err = d.Local.NodeTagDigests(ctx, image)
				if err == nil {
					perImage[image] = byNode
				}
			}
			out = append(out, d.check(ctx, res, image, byNode, err))
		}
	}
	return out, nil
}

func (d *Detector) check(ctx context.Context, res Resource, image string, byNode map[string][]string, localErr error) Finding {
	f := Finding{Resource: res, Image: image}
	if localErr != nil {
		f.Status, f.Err = StatusError, fmt.Errorf("local: %w", localErr)
		return f
	}
	f.Local = union(byNode)
	if len(f.Local) == 0 {
		f.Status = StatusNoLocal
		return f
	}
	running, err := d.Running.Running(ctx, res, image)
	if err != nil {
		f.Status, f.Err = StatusError, fmt.Errorf("running: %w", err)
		return f
	}
	if len(running) == 0 {
		f.Status = StatusNoRunning
		return f
	}
	for _, r := range running {
		want := f.Local // a Revision is not tied to a node
		if r.Node != "" {
			want = byNode[r.Node] // a Pod is compared with the node it runs on
		}
		if len(want) > 0 && !contains(want, r.Digest) {
			f.Stale = append(f.Stale, Staleness{Running: r, Want: want})
		}
	}
	f.Status = StatusInSync
	if len(f.Stale) > 0 {
		f.Status = StatusStale
	}
	return f
}

func union(byNode map[string][]string) []string {
	set := map[string]struct{}{}
	for _, ds := range byNode {
		for _, d := range ds {
			set[d] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
