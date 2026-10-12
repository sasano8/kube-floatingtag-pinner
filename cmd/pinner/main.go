// Command pinner collects images from annotated Kubernetes resources and
// reports where the remote registry has moved on from the local registry.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sasano8/kube-floatingtag-pinner/internal/collector"
	"github.com/sasano8/kube-floatingtag-pinner/internal/local"
	"github.com/sasano8/kube-floatingtag-pinner/internal/registry"
	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
)

const usage = `usage: pinner <once|run> [flags]

  once   collect one time and exit (suits a CronJob)
  run    collect repeatedly every --interval until stopped
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	mode := os.Args[1]
	if mode != "once" && mode != "run" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	kubeconfig := fs.String("kubeconfig", "", "path to kubeconfig (default: in-cluster, then ~/.kube/config)")
	namespace := fs.String("namespace", "", "namespace to watch (default: all)")
	annotation := fs.String("annotation", source.DefaultAnnotation, "annotation that marks target resources")
	onNew := fs.String("on-new", string(collector.OnNewRegister), "for images with no record: register|ignore")
	localKind := fs.String("local", "node", "local registry: node (container runtime image store of the nodes)")
	interval := fs.Duration("interval", 5*time.Minute, "run mode: time between passes")
	fs.Parse(os.Args[2:])

	policy, err := collector.ParseOnNew(*onNew)
	if err != nil {
		fatal(err)
	}
	cfg, err := restConfig(*kubeconfig)
	if err != nil {
		fatal(err)
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		fatal(err)
	}

	var loc collector.Local
	switch *localKind {
	case "node":
		loc = &local.Node{Client: client}
	default:
		fatal(fmt.Errorf("unknown --local %q (supported: node)", *localKind))
	}
	c := &collector.Collector{
		Source:   &source.Kube{Client: client, Namespace: *namespace, Annotation: *annotation},
		Registry: registry.Remote{},
		Local:    loc,
		OnNew:    policy,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if mode == "once" {
		if !pass(ctx, c) {
			os.Exit(1)
		}
		return
	}
	for {
		pass(ctx, c)
		select {
		case <-ctx.Done():
			return
		case <-time.After(*interval):
		}
	}
}

// pass runs one collection and logs the results. It reports whether it succeeded.
func pass(ctx context.Context, c *collector.Collector) bool {
	results, err := c.CollectOnce(ctx)
	if err != nil {
		slog.Error("collect failed", "err", err)
		return false
	}
	ok := true
	for _, r := range results {
		attrs := []any{"image", r.Image, "owners", r.Owners, "remote", r.Remote, "local", r.Local}
		switch {
		case r.Status == collector.StatusError:
			ok = false
			slog.Error("failed", append(attrs, "err", r.Err)...)
		case r.PullErr != nil && errors.Is(r.PullErr, collector.ErrPullNotImplemented):
			slog.Warn(string(r.Status)+": pull needed but not supported by this local", attrs...)
		case r.PullErr != nil:
			ok = false
			slog.Error(string(r.Status)+": pull failed", append(attrs, "err", r.PullErr)...)
		case r.Status == collector.StatusChanged || r.Status == collector.StatusNew:
			slog.Warn(string(r.Status), append(attrs, "pulled", r.Pulled)...)
		default:
			slog.Info(string(r.Status), attrs...)
		}
	}
	return ok
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
