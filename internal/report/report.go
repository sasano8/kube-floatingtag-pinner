// Package report is the one place that decides how findings are logged, for every command.
// Logging is separate from actions: every finding is reported, and the runtime options only
// filter what is shown (--log-level, --log-events). Actions (what is done to a resource)
// live in internal/trigger.
package report

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Event names a kind of finding. Both stages use the same names; what is compared differs
// (stage 1: remote vs local cache, stage 2: local cache vs what runs).
type Event string

const (
	Match      Event = "match"       // the two sides agree
	Mismatch   Event = "mismatch"    // they differ (which is newer is unknown)
	NotPulled  Event = "not-pulled"  // the local cache does not hold the tag
	NotRunning Event = "not-running" // stage 2: nothing running to compare
	Error      Event = "error"       // the check failed
)

// Level is fixed per kind of event, so --log-level means the same in every command:
// nothing to do = INFO, a difference = WARN, a failure = ERROR.
func (e Event) Level() slog.Level {
	switch e {
	case Mismatch, NotPulled:
		return slog.LevelWarn
	case Error:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Reporter logs events through slog, skipping those filtered out by Events.
// The level threshold is the handler's (see ParseLevel).
type Reporter struct {
	Events map[Event]bool // nil = every event
}

func (r Reporter) Emit(ctx context.Context, e Event, attrs ...any) {
	if r.Events != nil && !r.Events[e] {
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

// ParseEvents parses --log-events. Empty = every event. Names the command cannot emit are an error.
func ParseEvents(s string, allowed ...Event) (map[Event]bool, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	ok := map[Event]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	out := map[Event]bool{}
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !ok[Event(name)] {
			var names []string
			for _, a := range allowed {
				names = append(names, string(a))
			}
			return nil, fmt.Errorf("unknown event %q in --log-events (this command has: %s)", name, strings.Join(names, ", "))
		}
		out[Event(name)] = true
	}
	return out, nil
}
