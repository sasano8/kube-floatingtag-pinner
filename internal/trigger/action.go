package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// DefaultAction is used when a resource names no action for a state: nothing is done.
// (Every finding is logged anyway; see internal/report.)
const DefaultAction = "none"

// ActedOn are the states an action can be bound to (annotation on-<state>).
// Only mismatch is the one most users need; not-pulled and error are optional and also default to none,
// never to the mismatch action: with nothing to compare, acting blindly could repeat every pass.
var ActedOn = []Status{StatusMismatch, StatusNotPulled, StatusError}

// Action is what is done about a mismatch finding. The detection does not depend on it.
type Action interface {
	Name() string
	Supports(kind string) bool
	Do(ctx context.Context, f Finding) error
}

// None does nothing to the resource. Findings are reported by internal/report regardless.
type None struct{}

func (None) Name() string                      { return "none" }
func (None) Supports(string) bool              { return true }
func (None) Do(context.Context, Finding) error { return nil }

// Actor picks the action a resource names and runs it on its mismatch findings.
type Actor struct {
	actions map[string]Action
	// DryRun runs no action: it only logs which action would have run.
	DryRun bool
}

func NewActor(actions ...Action) *Actor {
	a := &Actor{actions: map[string]Action{}}
	for _, ac := range actions {
		a.actions[ac.Name()] = ac
	}
	return a
}

func (a *Actor) Act(ctx context.Context, f Finding) error {
	name := f.Actions[string(f.Status)]
	if name == "" {
		name = DefaultAction
	}
	ac, ok := a.actions[name]
	if !ok {
		return fmt.Errorf("%s: unknown or not implemented action %q (available: %s)", f.Resource, name, a.names())
	}
	if !ac.Supports(f.Kind) {
		return fmt.Errorf("%s: action %q does not support %s", f.Resource, name, f.Kind)
	}
	if a.DryRun && name != DefaultAction {
		slog.Info("trigger dry-run: would run action", "resource", f.Resource.String(), "image", f.Image,
			"state", string(f.Status), "action", name)
		return nil
	}
	if err := ac.Do(ctx, f); err != nil {
		return err
	}
	if name != DefaultAction {
		// Fire and forget: what the action causes is up to Kubernetes, and is not followed.
		// The next pass observes the new state.
		slog.Info("trigger action requested", "resource", f.Resource.String(), "image", f.Image,
			"state", string(f.Status), "action", name)
	}
	return nil
}

func (a *Actor) names() string {
	var n []string
	for k := range a.actions {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}
