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

func TestActorPicksActionByEventAnnotation(t *testing.T) {
	info, other := &recorder{name: "info"}, &recorder{name: "other"}
	a := trigger.NewActor(info, other)
	ctx := context.Background()

	f := trigger.Finding{Resource: trigger.Resource{Kind: "Deployment", Name: "d"}, Status: trigger.StatusMismatch,
		Mismatches: []trigger.Mismatch{{}}}
	if err := a.Act(ctx, f); err != nil || len(info.got) != 1 {
		t.Fatalf("default must be info: %v %d", err, len(info.got))
	}
	f.Actions = map[string]string{"mismatch": "other"}
	if err := a.Act(ctx, f); err != nil || len(other.got) != 1 {
		t.Fatalf("%v", err)
	}
	// on-mismatch is not inherited by not-pulled: it stays info
	f.Status = trigger.StatusNotPulled
	if err := a.Act(ctx, f); err != nil || len(other.got) != 1 || len(info.got) != 2 {
		t.Fatalf("not-pulled must default to info, not to the mismatch action: %v", err)
	}
	f.Actions["not-pulled"] = "other"
	if err := a.Act(ctx, f); err != nil || len(other.got) != 2 {
		t.Fatalf("%v", err)
	}
	a.ForceInfo = true
	if err := a.Act(ctx, f); err != nil || len(other.got) != 2 || len(info.got) != 3 {
		t.Fatalf("ForceInfo must run info: %v", err)
	}
	a.ForceInfo = false
	f.Actions["not-pulled"] = "missing"
	if err := a.Act(ctx, f); err == nil {
		t.Fatal("unknown action must be an error")
	}
	f.Actions["not-pulled"], f.Kind = "other", "Service"
	if err := a.Act(ctx, f); err == nil {
		t.Fatal("unsupported kind must be an error")
	}
}
