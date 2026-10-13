// Command pinner keeps what runs in Kubernetes in step with the floating tags
// (e.g. :latest) of the remote registry, in two stages: pull syncs the local cache
// (node image stores) from the remote registry; trigger finds the resources that run
// a different image than the local cache holds and hands them to their action.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
	"github.com/sasano8/kube-floatingtag-pinner/internal/local"
	"github.com/sasano8/kube-floatingtag-pinner/internal/registry"
	"github.com/sasano8/kube-floatingtag-pinner/internal/report"
	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
	"github.com/sasano8/kube-floatingtag-pinner/internal/trigger"
)

const usage = `usage: pinner <command> [flags]

commands:
  pull      stage 1: pull into the local cache (node image stores) what changed on the remote registry
  trigger   stage 2: find resources running a different image than the local cache, and run their action
`

// common holds the flags shared by every command.
type common struct {
	kubeconfig *string
	namespace  *string
	annotation *string
	floating   *string
	localKind  *string
	daemon     *bool
	interval   *time.Duration
	dryRun     *bool
	logLevel   *string
	logStates  *string
}

func registerCommon(fs *flag.FlagSet, dryRunHelp string) *common {
	return &common{
		kubeconfig: fs.String("kubeconfig", "", "path to kubeconfig (default: in-cluster, then ~/.kube/config)"),
		namespace:  fs.String("namespace", "", "namespace to watch (default: all)"),
		annotation: fs.String("annotation", source.DefaultAnnotation, "annotation that marks target resources"),
		floating:   fs.String("floating-tags", "latest", "comma-separated tags to track; images with other tags are ignored (empty = all tags)"),
		localKind:  fs.String("local", "node", "local cache: node (container runtime image store of the nodes)"),
		daemon:     fs.Bool("daemon", false, "keep running: repeat every --interval until stopped (default: run once and exit)"),
		interval:   fs.Duration("interval", 5*time.Minute, "with --daemon: time between passes"),
		dryRun:     fs.Bool("dry-run", false, dryRunHelp),
		logLevel:   fs.String("log-level", "info", "log level threshold: debug|info|warn|error (match, not-running = info; mismatch, not-pulled = warn; error = error)"),
		logStates:  fs.String("log-states", "", "comma-separated states to log (default: all): match, mismatch, not-pulled, error; trigger also not-running"),
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var j job
	switch os.Args[1] {
	case "pull":
		j = setupPull(os.Args[2:])
	case "trigger":
		j = setupTrigger(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	j.run()
}

// job is one command's pass, and whether to repeat it.
type job struct {
	pass     func(context.Context) bool // reports whether the pass succeeded
	daemon   bool
	interval time.Duration
}

// run executes one pass and exits non-zero on failure, or repeats it with --daemon.
func (j job) run() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !j.daemon {
		if !j.pass(ctx) {
			os.Exit(1)
		}
		return
	}
	for {
		j.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(j.interval):
		}
	}
}

func setupPull(args []string) job {
	fs := flag.NewFlagSet("pinner pull", flag.ExitOnError)
	c := registerCommon(fs, "report what would be pulled, without pulling")
	onNew := fs.String("on-new", string(collector.OnNewRegister), "for images the local cache does not hold: register (pull) | ignore")
	pullNS := fs.String("pull-namespace", "default", "namespace for the short-lived Pods that make nodes pull images")
	pullSecret := fs.String("pull-secret", "", "imagePullSecret name (in --pull-namespace) for private registries")
	pullTimeout := fs.Duration("pull-timeout", 2*time.Minute, "per-node timeout for a pull")
	fs.Parse(args)
	rep := setupLogging(c, report.Match, report.Mismatch, report.NotPulled, report.Error)

	policy, err := collector.ParseOnNew(*onNew)
	if err != nil {
		fatal(err)
	}
	client := newClient(*c.kubeconfig)
	col := &collector.Collector{
		Source:   &source.Kube{Client: client, Namespace: *c.namespace, Annotation: *c.annotation, FloatingTags: splitList(*c.floating)},
		Registry: registry.Remote{},
		Local:    newLocal(*c.localKind, &local.Node{Client: client, PullNamespace: *pullNS, PullSecret: *pullSecret, PullTimeout: *pullTimeout}),
		OnNew:    policy,
		DryRun:   *c.dryRun,
	}
	return job{pass: func(ctx context.Context) bool { return pullPass(ctx, col, rep) }, daemon: *c.daemon, interval: *c.interval}
}

func setupTrigger(args []string) job {
	fs := flag.NewFlagSet("pinner trigger", flag.ExitOnError)
	c := registerCommon(fs, "run no action: log which action would have run")
	fs.Parse(args)
	rep := setupLogging(c, report.Match, report.Mismatch, report.NotPulled, report.NotRunning, report.Error)

	client := newClient(*c.kubeconfig)
	det := &trigger.Detector{
		Source:  &source.Kube{Client: client, Namespace: *c.namespace, Annotation: *c.annotation, FloatingTags: splitList(*c.floating)},
		Local:   newLocal(*c.localKind, &local.Node{Client: client}),
		Running: &source.KubeRunning{Client: client},
	}
	actor := trigger.NewActor(trigger.None{})
	actor.DryRun = *c.dryRun
	return job{pass: func(ctx context.Context) bool { return triggerPass(ctx, det, actor, rep) }, daemon: *c.daemon, interval: *c.interval}
}

// setupLogging installs the logger at --log-level and returns the reporter for --log-states.
func setupLogging(c *common, allowed ...report.State) report.Reporter {
	level, err := report.ParseLevel(*c.logLevel)
	if err != nil {
		fatal(err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	states, err := report.ParseStates(*c.logStates, allowed...)
	if err != nil {
		fatal(err)
	}
	return report.Reporter{States: states}
}

func newClient(kubeconfig string) dynamic.Interface {
	cfg, err := restConfig(kubeconfig)
	if err != nil {
		fatal(err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		fatal(err)
	}
	return client
}

// newLocal returns the local cache for --local. Only "node" exists so far.
func newLocal(kind string, node *local.Node) *local.Node {
	if kind != "node" {
		fatal(fmt.Errorf("unknown --local %q (supported: node)", kind))
	}
	return node
}

// pullPass runs one collection and reports the results. It reports whether it succeeded.
// A pull shows as a change of state: the state found, then the state read back after pulling.
func pullPass(ctx context.Context, c *collector.Collector, rep report.Reporter) bool {
	results, err := c.CollectOnce(ctx)
	if err != nil {
		slog.Error("collect failed", "err", err)
		return false
	}
	ok := true
	for _, r := range results {
		attrs := []any{"stage", "pull", "image", r.Image, "owners", r.Owners, "remote", r.Remote, "local", r.Local}
		switch {
		case r.Status == collector.StatusError:
			ok = false
			rep.Emit(ctx, report.Error, append(attrs, "err", r.Err)...)
		case r.PullErr != nil && errors.Is(r.PullErr, collector.ErrPullNotImplemented):
			rep.Emit(ctx, report.State(r.Status), append(attrs, "outcome", "pull-unsupported")...)
		case r.PullErr != nil:
			ok = false
			rep.Emit(ctx, report.Error, append(attrs, "state", string(r.Status), "outcome", "pull-failed", "err", r.PullErr)...)
		default:
			if o := pullOutcome(r); o != "" {
				attrs = append(attrs, "outcome", o)
			}
			rep.Emit(ctx, report.State(r.Status), attrs...)
			if r.Pulled {
				after := []any{"stage", "pull", "image", r.Image, "owners", r.Owners, "remote", r.Remote, "local", r.AfterLocal, "via", "pulling"}
				if r.AfterErr != nil {
					ok = false
					rep.Emit(ctx, report.Error, append(after, "err", r.AfterErr)...)
				} else {
					rep.Emit(ctx, report.State(r.After), after...)
				}
			}
		}
	}
	return ok
}

// pullOutcome says what pull did about a difference ("" when there was nothing to do).
func pullOutcome(r collector.Result) string {
	switch {
	case r.Pulled:
		return "pulled"
	case r.DryRun:
		return "would-pull"
	case r.Ignored:
		return "ignored"
	}
	return ""
}

// triggerPass reports what it finds and hands the findings an action is bound to to the actor.
// It reports whether it succeeded.
func triggerPass(ctx context.Context, d *trigger.Detector, actor *trigger.Actor, rep report.Reporter) bool {
	findings, err := d.Detect(ctx)
	if err != nil {
		slog.Error("detect failed", "err", err)
		return false
	}
	ok := true
	for _, f := range findings {
		attrs := []any{"stage", "trigger", "resource", f.Resource.String(), "image", f.Image}
		switch f.Status {
		case trigger.StatusError:
			ok = false // the exit code reports it whatever the action does
			attrs = append(attrs, "err", f.Err)
		case trigger.StatusMismatch:
			attrs = append(attrs, "running", describe(f.Mismatches), "local", f.Mismatches[0].Want)
		case trigger.StatusNotPulled:
			attrs = append(attrs, "hint", "the local cache does not hold this tag (run pull first)")
		case trigger.StatusNotRunning:
			attrs = append(attrs, "hint", "nothing to compare (scaled to zero, or no digest reported)")
		default:
			attrs = append(attrs, "local", f.Local)
		}
		rep.Emit(ctx, report.State(f.Status), attrs...)

		switch f.Status {
		case trigger.StatusMismatch, trigger.StatusNotPulled, trigger.StatusError:
			if err := actor.Act(ctx, f); err != nil {
				ok = false
				slog.Error("action failed", "resource", f.Resource.String(), "image", f.Image, "err", err)
			}
		}
	}
	return ok
}

// describe lists the running digests that differ, e.g. "web-1=sha256:…,revision=sha256:…".
func describe(ms []trigger.Mismatch) string {
	parts := make([]string, 0, len(ms))
	for _, x := range ms {
		who := x.Pod
		if who == "" {
			who = "revision"
		}
		parts = append(parts, who+"="+x.Digest)
	}
	return strings.Join(parts, ",")
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func fatal(err error) {
	slog.Error("fatal", "err", err)
	os.Exit(1)
}

// restConfig uses --kubeconfig if given, else in-cluster, else ~/.kube/config.
func restConfig(path string) (*rest.Config, error) {
	if path != "" {
		return clientcmd.BuildConfigFromFlags("", path)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
}
