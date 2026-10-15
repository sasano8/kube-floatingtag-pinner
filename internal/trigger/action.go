package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/sasano8/pinner/internal/state"
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

	// Store remembers what was asked for, and how it went (see internal/state for the strategies it is
	// meant to carry). NewActor gives an in-memory one: forgotten when the process ends.
	Store state.Store
	// Now is the clock for the Records (default time.Now).
	Now func() time.Time

	count int
}

// BeginPass starts a new pass: the per-pass cap counts from zero.
// What was already requested is remembered across passes, in the Store.
func (a *Actor) BeginPass() { a.count = 0 }

func NewActor(actions ...Action) *Actor {
	a := &Actor{actions: map[string]Action{}, Store: state.NewMemory()}
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
	key := state.Key{Resource: f.Resource.String(), Image: f.Image, State: string(f.Status), Action: name}
	local := strings.Join(f.Local, ",")
	if name != DefaultAction {
		// The rule today is "once per local state": the same action for the same local digest is asked for
		// once. After a rollout the resource may still differ for a while (or for good); asking again every
		// pass would roll it out forever. The other strategies (cooldown, backoff, circuit breaker) are
		// described in internal/state, and would be decided here from the same Record.
		if rec, ok := a.Store.Get(key); ok && rec.Local == local {
			// Seen again in the same local state after asking: the action had no effect (yet).
			rec.StillDifferent++
			a.Store.Put(key, rec)
			slog.Debug("trigger action skipped: already requested for this local state",
				"resource", f.Resource.String(), "image", f.Image, "action", name, "still_different", rec.StillDifferent)
			return nil
		}
		if a.MaxActions > 0 && a.count >= a.MaxActions {
			slog.Warn("trigger action skipped: --max-actions reached for this pass",
				"resource", f.Resource.String(), "image", f.Image, "action", name)
			return nil
		}
		if !a.DryRun {
			// Remembered before asking, so a failing action is not retried every pass either.
			now := time.Now
			if a.Now != nil {
				now = a.Now
			}
			prev, _ := a.Store.Get(key)
			a.Store.Put(key, state.Record{Local: local, RequestedAt: now(), Failures: prev.Failures})
			a.count++
		}
	}
	if a.DryRun && name != DefaultAction {
		slog.Info("trigger dry-run: would run action", "resource", f.Resource.String(), "image", f.Image,
			"state", string(f.Status), "action", name)
		return nil
	}
	if err := ac.Do(ctx, f); err != nil {
		if name != DefaultAction {
			rec, _ := a.Store.Get(key)
			rec.Failures++
			rec.LastError = err.Error()
			a.Store.Put(key, rec)
		}
		return err
	}
	if name != DefaultAction {
		rec, _ := a.Store.Get(key)
		rec.Failures, rec.LastError = 0, ""
		a.Store.Put(key, rec)
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
