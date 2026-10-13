package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

func sample() []trigger.Resource {
	return []trigger.Resource{
		{
			Kind: "Deployment", Namespace: "ns", Name: "web",
			Images: []string{"r/app:latest"},
			Skipped: []trigger.Skipped{
				{Image: "r/fixed:v1.2.3", Reason: "tag is not floating (see --floating-tags)"},
				{Image: "r/side@sha256:abc", Reason: "pinned by digest"},
			},
			Actions: map[string]string{"mismatch": "rollout"},
		},
		{Kind: "Service", Namespace: "ns", Name: "ksvc", Revision: "ksvc-00002"},
	}
}

func TestWriteTable(t *testing.T) {
	var buf bytes.Buffer
	if err := writeTable(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"RESOURCE", "ON-MISMATCH",
		"ns/Deployment/web", "r/app:latest", "yes", "rollout",
		"r/fixed:v1.2.3", "no: tag is not floating",
		"r/side@sha256:abc", "no: pinned by digest",
		"ns/Service/ksvc", "no: the resource has no containers",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// the tracked image's row shows the action it names, and "-" for those it does not
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "r/app:latest") {
			f := strings.Fields(line)
			if len(f) != 6 || f[3] != "rollout" || f[4] != "-" || f[5] != "-" {
				t.Errorf("row = %q", line)
			}
		}
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var got []jsonResource
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	if len(got) != 2 || got[0].Resource != "ns/Deployment/web" || got[0].Images[0] != "r/app:latest" ||
		len(got[0].Skipped) != 2 || got[0].Actions["mismatch"] != "rollout" {
		t.Fatalf("%+v", got)
	}
	// empty lists and maps are [] and {}, not null, so a script can iterate them
	if got[1].Images == nil || got[1].Skipped == nil || got[1].Actions == nil {
		t.Fatalf("%+v", got[1])
	}
}
