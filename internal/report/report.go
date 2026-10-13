// Package report is the one place that decides how findings are logged, for every command.
// Logging is separate from actions: every finding is reported, and the runtime options only
// filter what is shown (--log-level, --log-states). Actions (what is done to a resource)
// live in internal/trigger.
package report

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// State names what a comparison found. Both stages use the same names; what is compared differs
// (stage 1: remote vs local cache, stage 2: local cache vs what runs). Stage 1 can move from one
// state to another by pulling (mismatch -> match); stage 2 only observes one and hands the rest
// to an action, whose effect it does not follow.
type State string

const (
	Match      State = "match"       // the two sides agree
	Mismatch   State = "mismatch"    // they differ (which is newer is unknown)
	NotPulled  State = "not-pulled"  // the local cache does not hold the tag
	NotRunning State = "not-running" // stage 2: nothing running to compare
	Error      State = "error"       // the check failed
)

// Level is fixed per state, so --log-level means the same in every command:
// nothing to do = INFO, a difference = WARN, a failure = ERROR.
func (e State) Level() slog.Level {
	switch e {
	case Mismatch, NotPulled:
		return slog.LevelWarn
	case Error:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Reporter logs states through slog, skipping those filtered out by States.
// The level threshold is the handler's (see ParseLevel).
type Reporter struct {
	States map[State]bool // nil = every state
}

func (r Reporter) Emit(ctx context.Context, e State, attrs ...any) {
	if r.States != nil && !r.States[e] {
		return
	}
	slog.Log(ctx, e.Level(), string(e), attrs...)
}

// ParseLevel parses --log-level.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(s))); err != nil {
		return 0, fmt.Errorf("unknown --log-level %q (want debug|info|warn|error)", s)
	}
	return l, nil
}

// ParseStates parses --log-states. Empty = every state. Names the command cannot emit are an error.
func ParseStates(s string, allowed ...State) (map[State]bool, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	ok := map[State]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	out := map[State]bool{}
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !ok[State(name)] {
			var names []string
			for _, a := range allowed {
				names = append(names, string(a))
			}
			return nil, fmt.Errorf("unknown state %q in --log-states (this command has: %s)", name, strings.Join(names, ", "))
		}
		out[State(name)] = true
	}
	return out, nil
}
