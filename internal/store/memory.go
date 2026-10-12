// Package store holds Store implementations (the local registry).
package store

import (
	"context"
	"sync"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
)

// Memory keeps the baseline in process memory; it is lost on restart.
type Memory struct {
	mu sync.Mutex
	m  map[string]collector.Entry
}

func NewMemory() *Memory { return &Memory{m: map[string]collector.Entry{}} }

func (s *Memory) Get(_ context.Context, image string) (collector.Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[image]
	return e, ok, nil
}

func (s *Memory) Put(_ context.Context, image string, e collector.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[image] = e
	return nil
}
