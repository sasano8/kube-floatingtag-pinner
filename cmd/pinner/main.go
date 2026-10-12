// Command pinner collects images from annotated Kubernetes resources and
// reports where the remote registry has moved on from the local registry.
package main

import (
	"context"
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
	"github.com/sasano8/kube-floatingtag-pinner/internal/registry"
	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
	"github.com/sasano8/kube-floatingtag-pinner/internal/store"
)

const usage = `usage: pinner <once|run> [flags]

  once   collect one time and exit (note: the in-memory store is empty each time)
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

	c := &collector.Collector{
		Source:   &source.Kube{Client: client, Namespace: *namespace, Annotation: *annotation},
		Registry: registry.Remote{},
		Store:    store.NewMemory(),
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
		attrs := []any{"image", r.Image, "owners", r.Owners}
		switch r.Status {
		case collector.StatusChanged:
			slog.Warn("digest changed", append(attrs, "old", r.Old, "new", r.New)...)
		case collector.StatusRegistered:
			slog.Info("registered", append(attrs, "digest", r.New)...)
		case collector.StatusError:
			ok = false
			slog.Error("failed", append(attrs, "err", r.Err)...)
		default:
			slog.Info(string(r.Status), append(attrs, "digest", r.New)...)
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
