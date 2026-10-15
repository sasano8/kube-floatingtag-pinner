package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/sasano8/pinner/internal/trigger"
)

// collectFlags are the flags of collect.
type collectFlags struct {
	*selection
	output *string
}

func registerCollect(fs *flag.FlagSet) *collectFlags {
	cf := &collectFlags{selection: registerSelection(fs), output: fs.String("output", "table", "output format: table|json")}
	fs.StringVar(cf.output, "o", "table", "shorthand for --output")
	return cf
}

// runCollect lists what the other commands start from: the annotated resources, the images they use,
// which of them are tracked (and why not, if not), and the actions their annotations name.
// It reads the cluster and prints; it changes nothing.
func runCollect(args []string) {
	fs := flag.NewFlagSet("pinner collect", flag.ExitOnError)
	cf := registerCollect(fs)
	parseFlags(fs, args)
	sel, output := cf.selection, cf.output
	if *output != "table" && *output != "json" {
		fatal(fmt.Errorf("unknown --output %q (want table|json)", *output))
	}

	resources, err := sel.kube(newClient(*sel.kubeconfig)).Resources(context.Background())
	if err != nil {
		fatal(err)
	}
	sort.SliceStable(resources, func(i, j int) bool { return resources[i].String() < resources[j].String() })

	if *output == "json" {
		err = writeJSON(os.Stdout, resources)
	} else {
		err = writeTable(os.Stdout, resources)
	}
	if err != nil {
		fatal(err)
	}
}

// writeTable prints one row per (resource, image).
func writeTable(w io.Writer, resources []trigger.Resource) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RESOURCE\tIMAGE\tTRACKED\tON-MISMATCH\tON-NOT-PULLED\tON-ERROR")
	action := func(r trigger.Resource, state string) string {
		if a := r.Actions[state]; a != "" {
			return a
		}
		return "-" // not set: the default, none
	}
	row := func(r trigger.Resource, image, tracked string) {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r, image, tracked,
			action(r, "mismatch"), action(r, "not-pulled"), action(r, "error"))
	}
	for _, r := range resources {
		for _, img := range r.Images {
			row(r, img, "yes")
		}
		for _, s := range r.Skipped {
			row(r, s.Image, "no: "+s.Reason)
		}
		if len(r.Images) == 0 && len(r.Skipped) == 0 {
			row(r, "-", "no: the resource has no containers")
		}
	}
	return tw.Flush()
}

type jsonResource struct {
	Resource  string            `json:"resource"`
	Kind      string            `json:"kind"`
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Images    []string          `json:"images"`  // tracked
	Skipped   []trigger.Skipped `json:"skipped"` // not tracked, and why
	Actions   map[string]string `json:"actions"`
	Selector  string            `json:"selector,omitempty"`
	Revision  string            `json:"revision,omitempty"`
}

func writeJSON(w io.Writer, resources []trigger.Resource) error {
	out := make([]jsonResource, 0, len(resources))
	for _, r := range resources {
		j := jsonResource{
			Resource: r.String(), Kind: r.Kind, Namespace: r.Namespace, Name: r.Name,
			Images: r.Images, Skipped: r.Skipped, Actions: r.Actions, Selector: r.Selector, Revision: r.Revision,
		}
		if j.Images == nil {
			j.Images = []string{}
		}
		if j.Skipped == nil {
			j.Skipped = []trigger.Skipped{}
		}
		if j.Actions == nil {
			j.Actions = map[string]string{}
		}
		out = append(out, j)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
