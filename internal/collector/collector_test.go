package collector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
	"github.com/sasano8/kube-floatingtag-pinner/internal/store"
)

type fakeSource []collector.Target

func (f fakeSource) Targets(context.Context) ([]collector.Target, error) { return f, nil }

type fakeRegistry map[string]string

func (f fakeRegistry) Digest(_ context.Context, image string) (string, error) {
	d, ok := f[image]
	if !ok {
		return "", errors.New("not found")
	}
	return d, nil
}

func run(t *testing.T, c *collector.Collector) collector.Result {
	t.Helper()
	rs, err := c.CollectOnce(context.Background())
	if err != nil || len(rs) != 1 {
		t.Fatalf("results=%v err=%v", rs, err)
	}
	return rs[0]
}

func TestCollectOnce(t *testing.T) {
	const img = "example.com/app:latest"
	reg := fakeRegistry{img: "sha256:a"}
	c := &collector.Collector{
		Source:   fakeSource{{Image: img, Owners: []string{"ns/Deployment/app"}}},
		Registry: reg,
		Store:    store.NewMemory(),
		OnNew:    collector.OnNewRegister,
	}

	if r := run(t, c); r.Status != collector.StatusRegistered || r.New != "sha256:a" {
		t.Fatalf("first: %+v", r)
	}
	if r := run(t, c); r.Status != collector.StatusUnchanged {
		t.Fatalf("second: %+v", r)
	}
	reg[img] = "sha256:b"
	if r := run(t, c); r.Status != collector.StatusChanged || r.Old != "sha256:a" || r.New != "sha256:b" {
		t.Fatalf("changed: %+v", r)
	}
	if r := run(t, c); r.Status != collector.StatusUnchanged {
		t.Fatalf("baseline not updated: %+v", r)
	}
}

func TestOnNewIgnore(t *testing.T) {
	c := &collector.Collector{
		Source:   fakeSource{{Image: "example.com/app:latest"}},
		Registry: fakeRegistry{"example.com/app:latest": "sha256:a"},
		Store:    store.NewMemory(),
		OnNew:    collector.OnNewIgnore,
	}
	if r := run(t, c); r.Status != collector.StatusIgnored {
		t.Fatalf("%+v", r)
	}
	if _, found, _ := c.Store.Get(context.Background(), "example.com/app:latest"); found {
		t.Fatal("ignored image must not be recorded")
	}
}

func TestRegistryErrorKeepsBaseline(t *testing.T) {
	st := store.NewMemory()
	st.Put(context.Background(), "x:latest", collector.Entry{Digest: "sha256:a"})
	c := &collector.Collector{
		Source: fakeSource{{Image: "x:latest"}}, Registry: fakeRegistry{}, Store: st, OnNew: collector.OnNewRegister,
	}
	r := run(t, c)
	if r.Status != collector.StatusError {
		t.Fatalf("%+v", r)
	}
	if e, _, _ := st.Get(context.Background(), "x:latest"); e.Digest != "sha256:a" {
		t.Fatal("baseline must survive a registry error")
	}
}
