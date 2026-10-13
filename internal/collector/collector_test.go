package collector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
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

type fakeLocal struct {
	have      []string
	pullErr   error
	pulledDig []string
}

func (f *fakeLocal) TagDigests(context.Context, string) ([]string, error) { return f.have, nil }
func (f *fakeLocal) Pull(_ context.Context, _, digest string) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	f.pulledDig = append(f.pulledDig, digest)
	return nil
}

const img = "example.com/app:latest"

func run(t *testing.T, loc *fakeLocal, onNew collector.OnNew) collector.Result {
	t.Helper()
	c := &collector.Collector{
		Source:   fakeSource{{Image: img, Owners: []string{"ns/Deployment/app"}}},
		Registry: fakeRegistry{img: "sha256:b"},
		Local:    loc,
		OnNew:    onNew,
	}
	rs, err := c.CollectOnce(context.Background())
	if err != nil || len(rs) != 1 {
		t.Fatalf("results=%v err=%v", rs, err)
	}
	return rs[0]
}

func TestUnchanged(t *testing.T) {
	loc := &fakeLocal{have: []string{"sha256:b"}}
	if r := run(t, loc, collector.OnNewRegister); r.Status != collector.StatusMatch || len(loc.pulledDig) != 0 {
		t.Fatalf("%+v %v", r, loc.pulledDig)
	}
}

func TestChangedPulls(t *testing.T) {
	loc := &fakeLocal{have: []string{"sha256:a"}}
	r := run(t, loc, collector.OnNewIgnore) // on-new does not apply to a known repository
	if r.Status != collector.StatusMismatch || !r.Pulled || loc.pulledDig[0] != "sha256:b" {
		t.Fatalf("%+v %v", r, loc.pulledDig)
	}
}

func TestNewRegisterPulls(t *testing.T) {
	loc := &fakeLocal{}
	if r := run(t, loc, collector.OnNewRegister); r.Status != collector.StatusNotPulled || !r.Pulled {
		t.Fatalf("%+v", r)
	}
}

func TestNewIgnoreDoesNothing(t *testing.T) {
	loc := &fakeLocal{}
	if r := run(t, loc, collector.OnNewIgnore); r.Status != collector.StatusNotPulled || !r.Ignored || r.Pulled || len(loc.pulledDig) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestPullNotImplementedIsReported(t *testing.T) {
	loc := &fakeLocal{have: []string{"sha256:a"}, pullErr: collector.ErrPullNotImplemented}
	r := run(t, loc, collector.OnNewRegister)
	if r.Status != collector.StatusMismatch || r.Pulled || !errors.Is(r.PullErr, collector.ErrPullNotImplemented) {
		t.Fatalf("%+v", r)
	}
}

// The tag differs between nodes: some already at the remote digest, some not.
func TestTagDivergedAcrossNodesIsChanged(t *testing.T) {
	loc := &fakeLocal{have: []string{"sha256:a", "sha256:b"}}
	if r := run(t, loc, collector.OnNewRegister); r.Status != collector.StatusMismatch || !r.Pulled {
		t.Fatalf("%+v", r)
	}
}

func TestDryRunDoesNotPull(t *testing.T) {
	for _, have := range [][]string{nil, {"sha256:a"}} { // new, changed
		loc := &fakeLocal{have: have}
		c := &collector.Collector{
			Source:   fakeSource{{Image: img}},
			Registry: fakeRegistry{img: "sha256:b"},
			Local:    loc,
			OnNew:    collector.OnNewRegister,
			DryRun:   true,
		}
		rs, err := c.CollectOnce(context.Background())
		if err != nil || len(rs) != 1 {
			t.Fatalf("%v %v", rs, err)
		}
		if r := rs[0]; !r.DryRun || r.Pulled || len(loc.pulledDig) != 0 {
			t.Fatalf("have=%v: %+v pulled=%v", have, r, loc.pulledDig)
		}
	}
}
