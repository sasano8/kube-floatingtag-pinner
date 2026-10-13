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

// StateAware is implemented by an action that only makes sense for some states
// (e.g. delete-pods acts on the Pods that differ, so only on mismatch).
type StateAware interface {
	States() []Status
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
	// MaxActions caps the actions requested per pass (0 = no cap), to limit the damage of a mistake.
	MaxActions int

	requested map[string]bool // already asked for this local state: not asked again
	count     int
}

// BeginPass starts a new pass: the per-pass cap counts from zero.
// What was already requested is remembered across passes (for the life of the process).
func (a *Actor) BeginPass() { a.count = 0 }

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
	if sa, ok := ac.(StateAware); ok && !containsStatus(sa.States(), f.Status) {
		return fmt.Errorf("%s: action %q cannot be bound to %s", f.Resource, name, f.Status)
	}
	if name != DefaultAction {
		// The same action for the same local state is asked for once. After a rollout the resource may
		// still differ for a while (or for good); asking again every pass would roll it out forever.
		key := fmt.Sprintf("%s|%s|%s|%s|%v", f.Resource, f.Image, f.Status, name, f.Local)
		if a.requested[key] {
			slog.Debug("trigger action skipped: already requested for this local state",
				"resource", f.Resource.String(), "image", f.Image, "action", name)
			return nil
		}
		if a.MaxActions > 0 && a.count >= a.MaxActions {
			slog.Warn("trigger action skipped: --max-actions reached for this pass",
				"resource", f.Resource.String(), "image", f.Image, "action", name)
			return nil
		}
		if !a.DryRun {
			if a.requested == nil {
				a.requested = map[string]bool{}
			}
			a.requested[key] = true
			a.count++
		}
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

func containsStatus(list []Status, s Status) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
