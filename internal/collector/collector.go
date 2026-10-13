// Package collector syncs the local cache (the image stores of the nodes) with the
// remote registry for the images referenced by annotated Kubernetes resources: the
// remote is the source of truth, and only the floating tag's digest is compared.
// It does not depend on how it is run (once, daemon, operator).
// See docs/design.md for the reasoning.
package collector

import (
	"context"
	"errors"
	"fmt"
)

// Target is an image to track together with the resources that reference it.
type Target struct {
	Image  string   // e.g. ghcr.io/org/app:latest
	Owners []string // e.g. ["ns/Deployment/name"]
}

// Source lists the images of annotated Kubernetes resources.
type Source interface {
	Targets(ctx context.Context) ([]Target, error)
}

// Registry resolves an image reference to its current digest (remote registry).
type Registry interface {
	Digest(ctx context.Context, image string) (string, error)
}

// ErrPullNotImplemented is returned by a Local that cannot bring images in yet.
var ErrPullNotImplemented = errors.New("pull is not implemented for this local")

// Local is the local cache of images: where images actually live on this side
// (e.g. the container runtime image store of the nodes, or a registry we sync to).
// It holds the images themselves; nothing is recorded separately.
type Local interface {
	// TagDigests lists the digests the image's tag points to locally (one per
	// distinct value, over the places that hold the tag). Empty = the tag is not
	// held locally at all. Digests held without the tag are not reported.
	TagDigests(ctx context.Context, image string) ([]string, error)
	// Pull brings the image at the given digest into the local cache.
	Pull(ctx context.Context, image, digest string) error
}

// OnNew decides what to do with an image the store has no record of.
type OnNew string

const (
	// OnNewRegister pulls the image into the local cache.
	OnNewRegister OnNew = "register"
	// OnNewIgnore does nothing for an image the local registry has never held.
	OnNewIgnore OnNew = "ignore"
)

func ParseOnNew(s string) (OnNew, error) {
	switch OnNew(s) {
	case OnNewRegister, OnNewIgnore:
		return OnNew(s), nil
	}
	return "", fmt.Errorf("unknown on-new %q (want register|ignore)", s)
}

// Status is the outcome for one image.
type Status string

const (
	// The names are shared with stage 2 (internal/report).
	StatusMatch     Status = "match"      // the local tag points to the remote digest
	StatusMismatch  Status = "mismatch"   // the local tag points to a different digest than the remote
	StatusNotPulled Status = "not-pulled" // the local cache does not hold the tag; handled per OnNew
	StatusError     Status = "error"
)

type Result struct {
	Target
	Status  Status
	Remote  string   // digest on the remote registry
	Local   []string // digests the tag points to locally
	Pulled  bool
	Ignored bool  // not-pulled, and OnNew is ignore: left alone
	DryRun  bool  // a pull was needed but skipped (Collector.DryRun)
	PullErr error // set when a pull was attempted and failed
	Err     error
}

type Collector struct {
	Source   Source
	Registry Registry
	Local    Local
	OnNew    OnNew

	// DryRun reports what would be pulled without pulling. Everything else is read-only anyway.
	DryRun bool
}

// CollectOnce runs one pass. Per-image failures are reported in the results;
// only a failure to list targets aborts the pass.
func (c *Collector) CollectOnce(ctx context.Context) ([]Result, error) {
	targets, err := c.Source.Targets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	results := make([]Result, 0, len(targets))
	for _, t := range targets {
		results = append(results, c.collect(ctx, t))
	}
	return results, nil
}

func (c *Collector) collect(ctx context.Context, t Target) Result {
	r := Result{Target: t}
	fail := func(err error) Result { r.Status, r.Err = StatusError, err; return r }

	remote, err := c.Registry.Digest(ctx, t.Image)
	if err != nil {
		return fail(fmt.Errorf("registry digest: %w", err))
	}
	r.Remote = remote

	local, err := c.Local.TagDigests(ctx, t.Image)
	if err != nil {
		return fail(fmt.Errorf("local digests: %w", err))
	}
	r.Local = local

	switch {
	case len(local) == 1 && local[0] == remote:
		r.Status = StatusMatch
		return r
	case len(local) == 0:
		if c.OnNew == OnNewIgnore {
			r.Status, r.Ignored = StatusNotPulled, true
			return r
		}
		r.Status = StatusNotPulled
	default:
		r.Status = StatusMismatch
	}

	if c.DryRun {
		r.DryRun = true
		return r
	}
	if err := c.Local.Pull(ctx, t.Image, remote); err != nil {
		r.PullErr = err
		return r
	}
	r.Pulled = true
	return r
}
