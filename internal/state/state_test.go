package state_test

import (
	"testing"
	"time"

	"github.com/sasano8/kube-floatingtag-pinner/internal/state"
)

func TestMemory(t *testing.T) {
	s := state.NewMemory()
	k := state.Key{Resource: "ns/Deployment/web", Image: "r/app:latest", State: "mismatch", Action: "rollout"}
	if _, ok := s.Get(k); ok {
		t.Fatal("empty at first")
	}
	want := state.Record{Local: "sha256:B", RequestedAt: time.Unix(1, 0), Failures: 2, StillDifferent: 1, LastError: "boom"}
	s.Put(k, want)
	if got, ok := s.Get(k); !ok || got != want {
		t.Fatalf("%+v %v", got, ok)
	}
	other := k
	other.Action = "delete-pods"
	if _, ok := s.Get(other); ok {
		t.Fatal("keys differ by action")
	}
	s.Delete(k)
	if _, ok := s.Get(k); ok {
		t.Fatal("deleted")
	}
}
