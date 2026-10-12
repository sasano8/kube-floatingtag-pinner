// Package collector compares the remote registry with the local registry
// (store) for the images referenced by annotated Kubernetes resources.
// It does not depend on how it is run (once, loop, operator).
package collector

import (
	"context"
	"fmt"
	"time"
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

// Entry is what the local registry holds for an image.
type Entry struct {
	Digest    string
	UpdatedAt time.Time
}

// Store is the local registry: the digest we currently regard as the baseline.
type Store interface {
	Get(ctx context.Context, image string) (Entry, bool, error)
	Put(ctx context.Context, image string, e Entry) error
}

// OnNew decides what to do with an image the store has no record of.
type OnNew string

const (
	// OnNewRegister records the remote digest as the baseline (no diff reported).
	OnNewRegister OnNew = "register"
	// OnNewIgnore leaves the image unrecorded and reports nothing.
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
	StatusRegistered Status = "registered" // first seen, baseline recorded
	StatusIgnored    Status = "ignored"    // first seen, left alone
	StatusUnchanged  Status = "unchanged"
	StatusChanged    Status = "changed" // remote digest differs from the store
	StatusError      Status = "error"
)

type Result struct {
	Target
	Status Status
	Old    string // digest in the store before this run
	New    string // digest on the remote registry
	Err    error
}

type Collector struct {
	Source   Source
	Registry Registry
	Store    Store
	OnNew    OnNew
	Now      func() time.Time
}

// CollectOnce runs one pass. Per-image failures are reported in the results;
// only a failure to list targets aborts the pass.
func (c *Collector) CollectOnce(ctx context.Context) ([]Result, error) {
	targets, err := c.Source.Targets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	results := make([]Result, 0, len(targets))
	for _, t := range targets {
		results = append(results, c.collect(ctx, t, now()))
	}
	return results, nil
}

func (c *Collector) collect(ctx context.Context, t Target, now time.Time) Result {
	r := Result{Target: t}
	fail := func(err error) Result { r.Status, r.Err = StatusError, err; return r }

	old, found, err := c.Store.Get(ctx, t.Image)
	if err != nil {
		return fail(fmt.Errorf("store get: %w", err))
	}
	r.Old = old.Digest

	if !found && c.OnNew == OnNewIgnore {
		r.Status = StatusIgnored
		return r
	}

	digest, err := c.Registry.Digest(ctx, t.Image)
	if err != nil {
		return fail(fmt.Errorf("registry digest: %w", err))
	}
	r.New = digest

	switch {
	case !found:
		r.Status = StatusRegistered
	case old.Digest == digest:
		r.Status = StatusUnchanged
		return r
	default:
		r.Status = StatusChanged
	}
	if err := c.Store.Put(ctx, t.Image, Entry{Digest: digest, UpdatedAt: now}); err != nil {
		return fail(fmt.Errorf("store put: %w", err))
	}
	return r
}
