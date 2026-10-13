package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// DefaultAction is used when a resource names no action: it only reports.
const DefaultAction = "info"

// Action is what is done about a mismatch finding. The detection does not depend on it.
type Action interface {
	Name() string
	Supports(kind string) bool
	Do(ctx context.Context, f Finding) error
}

// Info logs that what runs differs from the local cache. It changes nothing.
type Info struct{}

func (Info) Name() string         { return "info" }
func (Info) Supports(string) bool { return true }
func (Info) Do(_ context.Context, f Finding) error {
	var running []string
	for _, s := range f.Mismatches {
		who := s.Pod
		if who == "" {
			who = "revision"
		}
		running = append(running, fmt.Sprintf("%s=%s", who, s.Digest))
	}
	slog.Warn("mismatch", "resource", f.Resource.String(), "image", f.Image,
		"running", strings.Join(running, ","), "local", f.Mismatches[0].Want)
	return nil
}

// None does nothing at all.
type None struct{}

func (None) Name() string                      { return "none" }
func (None) Supports(string) bool              { return true }
func (None) Do(context.Context, Finding) error { return nil }

// Actor picks the action a resource names and runs it on its mismatch findings.
type Actor struct {
	actions map[string]Action
	// ForceInfo runs every action as info (a dry run for the whole pass).
	ForceInfo bool
}

func NewActor(actions ...Action) *Actor {
	a := &Actor{actions: map[string]Action{}}
	for _, ac := range actions {
		a.actions[ac.Name()] = ac
	}
	return a
}

func (a *Actor) Act(ctx context.Context, f Finding) error {
	name := f.Action
	if name == "" || a.ForceInfo {
		name = DefaultAction
	}
	ac, ok := a.actions[name]
	if !ok {
		return fmt.Errorf("%s: unknown or not implemented action %q (available: %s)", f.Resource, name, a.names())
	}
	if !ac.Supports(f.Kind) {
		return fmt.Errorf("%s: action %q does not support %s", f.Resource, name, f.Kind)
	}
	return ac.Do(ctx, f)
}

func (a *Actor) names() string {
	var n []string
	for k := range a.actions {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}
