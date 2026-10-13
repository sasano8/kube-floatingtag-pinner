package trigger_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

type fakeSource []trigger.Resource

func (f fakeSource) Resources(context.Context) ([]trigger.Resource, error) { return f, nil }

type fakeLocal map[string]map[string][]string // image -> node -> digests

func (f fakeLocal) NodeTagDigests(_ context.Context, image string) (map[string][]string, error) {
	return f[image], nil
}

type fakeRunning struct {
	byRes map[string][]trigger.Running
	err   error
}

func (f fakeRunning) Running(_ context.Context, r trigger.Resource, _ string) ([]trigger.Running, error) {
	return f.byRes[r.Name], f.err
}

const img = "ghcr.io/o/app:latest"

func detect(t *testing.T, local fakeLocal, running fakeRunning, res ...trigger.Resource) []trigger.Finding {
	t.Helper()
	d := &trigger.Detector{Source: fakeSource(res), Local: local, Running: running}
	fs, err := d.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func deploy(name string) trigger.Resource {
	return trigger.Resource{Kind: "Deployment", Namespace: "ns", Name: name, Images: []string{img}}
}

func TestPodComparedWithItsOwnNode(t *testing.T) {
	local := fakeLocal{img: {"n1": {"sha256:B"}, "n2": {"sha256:A"}}}
	running := fakeRunning{byRes: map[string][]trigger.Running{
		"web": {
			{Digest: "sha256:B", Node: "n1", Pod: "web-1"}, // current on n1
			{Digest: "sha256:B", Node: "n2", Pod: "web-2"}, // n2's tag is still A, B is not what n2 holds
		},
	}}
	f := detect(t, local, running, deploy("web"))[0]
	if f.Status != trigger.StatusMismatch || len(f.Mismatches) != 1 || f.Mismatches[0].Pod != "web-2" ||
		!reflect.DeepEqual(f.Mismatches[0].Want, []string{"sha256:A"}) {
		t.Fatalf("%+v", f)
	}
}

func TestMatch(t *testing.T) {
	local := fakeLocal{img: {"n1": {"sha256:B"}}}
	running := fakeRunning{byRes: map[string][]trigger.Running{"web": {{Digest: "sha256:B", Node: "n1", Pod: "web-1"}}}}
	if f := detect(t, local, running, deploy("web"))[0]; f.Status != trigger.StatusMatch {
		t.Fatalf("%+v", f)
	}
}

func TestRevisionComparedWithLocalUnion(t *testing.T) {
	local := fakeLocal{img: {"n1": {"sha256:B"}, "n2": {"sha256:B"}}}
	ksvc := trigger.Resource{Kind: "Service", Namespace: "ns", Name: "k", Images: []string{img}}
	differs := fakeRunning{byRes: map[string][]trigger.Running{"k": {{Digest: "sha256:A"}}}}
	if f := detect(t, local, differs, ksvc)[0]; f.Status != trigger.StatusMismatch {
		t.Fatalf("%+v", f)
	}
	fresh := fakeRunning{byRes: map[string][]trigger.Running{"k": {{Digest: "sha256:B"}}}}
	if f := detect(t, local, fresh, ksvc)[0]; f.Status != trigger.StatusMatch {
		t.Fatalf("%+v", f)
	}
}

func TestNoLocalAndNoRunning(t *testing.T) {
	if f := detect(t, fakeLocal{}, fakeRunning{}, deploy("web"))[0]; f.Status != trigger.StatusNotPulled {
		t.Fatalf("%+v", f)
	}
	local := fakeLocal{img: {"n1": {"sha256:B"}}}
	if f := detect(t, local, fakeRunning{}, deploy("web"))[0]; f.Status != trigger.StatusNotRunning {
		t.Fatalf("%+v", f)
	}
}

func TestRunningErrorIsReported(t *testing.T) {
	local := fakeLocal{img: {"n1": {"sha256:B"}}}
	f := detect(t, local, fakeRunning{err: errors.New("boom")}, deploy("web"))[0]
	if f.Status != trigger.StatusError || f.Err == nil {
		t.Fatalf("%+v", f)
	}
}

type recorder struct {
	name string
	got  []trigger.Finding
}

func (r *recorder) Name() string              { return r.name }
func (r *recorder) Supports(kind string) bool { return kind != "Service" }
func (r *recorder) Do(_ context.Context, f trigger.Finding) error {
	r.got = append(r.got, f)
	return nil
}

func TestActorPicksActionByStateAnnotation(t *testing.T) {
	none, other := &recorder{name: "none"}, &recorder{name: "other"}
	a := trigger.NewActor(none, other)
	ctx := context.Background()

	f := trigger.Finding{Resource: trigger.Resource{Kind: "Deployment", Name: "d"}, Status: trigger.StatusMismatch,
		Mismatches: []trigger.Mismatch{{}}}
	if err := a.Act(ctx, f); err != nil || len(none.got) != 1 {
		t.Fatalf("default must be none: %v %d", err, len(none.got))
	}
	f.Actions = map[string]string{"mismatch": "other"}
	if err := a.Act(ctx, f); err != nil || len(other.got) != 1 {
		t.Fatalf("%v", err)
	}
	// on-mismatch is not inherited by not-pulled: it stays none
	f.Status = trigger.StatusNotPulled
	if err := a.Act(ctx, f); err != nil || len(other.got) != 1 || len(none.got) != 2 {
		t.Fatalf("not-pulled must default to none, not to the mismatch action: %v", err)
	}
	f.Actions["not-pulled"] = "other"
	if err := a.Act(ctx, f); err != nil || len(other.got) != 2 {
		t.Fatalf("%v", err)
	}
	a.DryRun = true
	if err := a.Act(ctx, f); err != nil || len(other.got) != 2 {
		t.Fatalf("a dry run must not run the action: %v", err)
	}
	a.DryRun = false
	f.Actions["not-pulled"] = "missing"
	if err := a.Act(ctx, f); err == nil {
		t.Fatal("unknown action must be an error")
	}
	f.Actions["not-pulled"], f.Kind = "other", "Service"
	if err := a.Act(ctx, f); err == nil {
		t.Fatal("unsupported kind must be an error")
	}
}

func TestErrorFindingGoesToNoneByDefault(t *testing.T) {
	none := &recorder{name: "none"}
	a := trigger.NewActor(none)
	f := trigger.Finding{Resource: trigger.Resource{Kind: "Deployment", Name: "d"}, Status: trigger.StatusError, Err: errors.New("boom")}
	if err := a.Act(context.Background(), f); err != nil || len(none.got) != 1 {
		t.Fatalf("%v %d", err, len(none.got))
	}
}

func mismatch(name string, local ...string) trigger.Finding {
	return trigger.Finding{
		Resource: trigger.Resource{Kind: "Deployment", Name: name, Actions: map[string]string{"mismatch": "other"}},
		Image:    img, Status: trigger.StatusMismatch, Local: local, Mismatches: []trigger.Mismatch{{}},
	}
}

func TestActionIsRequestedOncePerLocalState(t *testing.T) {
	none, other := &recorder{name: "none"}, &recorder{name: "other"}
	a := trigger.NewActor(none, other)
	ctx := context.Background()
	a.BeginPass()
	for range 3 { // three passes of a daemon: the resource still differs
		if err := a.Act(ctx, mismatch("web", "sha256:B")); err != nil {
			t.Fatal(err)
		}
		a.BeginPass()
	}
	if len(other.got) != 1 {
		t.Fatalf("asked %d times for the same local state", len(other.got))
	}
	if err := a.Act(ctx, mismatch("web", "sha256:C")); err != nil || len(other.got) != 2 {
		t.Fatalf("a new local digest is a new state: %v %d", err, len(other.got))
	}
	if err := a.Act(ctx, mismatch("api", "sha256:C")); err != nil || len(other.got) != 3 {
		t.Fatalf("another resource is asked separately: %v %d", err, len(other.got))
	}
}

func TestDryRunIsNotRemembered(t *testing.T) {
	other := &recorder{name: "other"}
	a := trigger.NewActor(&recorder{name: "none"}, other)
	a.DryRun = true
	a.Act(context.Background(), mismatch("web", "sha256:B"))
	a.DryRun = false
	if err := a.Act(context.Background(), mismatch("web", "sha256:B")); err != nil || len(other.got) != 1 {
		t.Fatalf("a dry run must not use up the request: %v %d", err, len(other.got))
	}
}

func TestMaxActionsCapsAPass(t *testing.T) {
	other := &recorder{name: "other"}
	a := trigger.NewActor(&recorder{name: "none"}, other)
	a.MaxActions = 2
	ctx := context.Background()
	a.BeginPass()
	for _, n := range []string{"a", "b", "c"} {
		a.Act(ctx, mismatch(n, "sha256:B"))
	}
	if len(other.got) != 2 {
		t.Fatalf("cap of 2: asked %d", len(other.got))
	}
	a.BeginPass() // the next pass counts from zero, and the skipped one was not remembered
	a.Act(ctx, mismatch("c", "sha256:B"))
	if len(other.got) != 3 {
		t.Fatalf("asked %d", len(other.got))
	}
}
