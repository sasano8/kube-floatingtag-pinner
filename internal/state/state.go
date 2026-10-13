// Package state keeps what the actor has done, so it does not do the same thing over and over.
// It is not a baseline for comparisons (the remote registry and the local cache are the sources of
// truth, see docs/design.md): it only remembers which actions were asked for, and how they went.
// Losing it is harmless: the worst case is one more action request.
//
// Today the only Store is in memory (a map). So it is forgotten when the process ends: a `trigger`
// run once at a time, or a daemon that restarts, starts from nothing. Other Stores can be added behind
// the interface without touching the actor.
//
// # Strategies this store is meant to carry (not implemented yet)
//
// The actor decides "may I act?" from a Record. Today that is one rule, "once per local state".
// More rules fit in the same place, each reading and writing Records:
//
//   - once-per-state (today): the same (resource, image, state, action) is asked for once per local
//     digest. Record.Local is the local digest(s) it was asked for.
//   - cooldown: at least N minutes between two actions on one resource (Record.RequestedAt).
//   - backoff: the more an action fails or has no effect, the longer the wait: 1m, 2m, 4m, ... up to a
//     cap (Record.Failures, Record.StillDifferent). Updates get more reluctant where they do not work.
//   - circuit breaker: if failures in one pass pass a threshold (no permission, the API is down),
//     stop acting for the rest of the pass, and for a while.
//   - the per-pass cap (--max-actions) stays as a separate guard.
//
// What counts as "it did not work": trigger is fire-and-forget and does not follow the result, but the
// next pass sees the resource again. Asked, and the same local digest still differs = no effect
// (Record.StillDifferent); asked, and it matches now = it worked (the Record can be reset).
// An API error when asking = a failure (Record.Failures).
//
// # Where a persistent Store could live
//
//   - a file (JSON): for a trigger run by hand or by cron, one process after another; no dependency needed
//     at this size (a few records per annotated resource). An embedded key-value database such as bbolt is
//     only worth it if several processes write at once, or the data grows large.
//   - a ConfigMap: for running inside the cluster (about 1 MiB per object; writes can conflict).
//   - not an annotation on the resource: that would write to the user's resource.
package state

import (
	"sync"
	"time"
)

// Key identifies one thing the actor may do: this action, for this state of this image of this resource.
type Key struct {
	Resource string // e.g. ns/Deployment/web
	Image    string
	State    string // the state the action is bound to: mismatch, not-pulled, error
	Action   string
}

// Record is what is remembered about a Key.
type Record struct {
	// Local is the local digest(s) the action was asked for, joined: the "state" of the once-per-state rule.
	Local string
	// RequestedAt is when the action was last asked for.
	RequestedAt time.Time
	// Failures counts the times asking failed (an API error), in a row.
	Failures int
	// StillDifferent counts the passes that saw the same local state again after the action was asked for:
	// the action had no effect (yet).
	StillDifferent int
	// LastError is the last error from asking, for diagnosis.
	LastError string
}

// Store keeps Records. Implementations must be safe for concurrent use.
type Store interface {
	Get(k Key) (Record, bool)
	Put(k Key, r Record)
	Delete(k Key)
}

// Memory is a Store in a map. It is forgotten when the process ends.
type Memory struct {
	mu sync.Mutex
	m  map[Key]Record
}

func NewMemory() *Memory { return &Memory{m: map[Key]Record{}} }

func (s *Memory) Get(k Key) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[k]
	return r, ok
}

func (s *Memory) Put(k Key, r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = r
}

func (s *Memory) Delete(k Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
}
