// Command pinner collects images from annotated Kubernetes resources and
// syncs the local cache (node image stores) with the remote registry.
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
	"github.com/sasano8/kube-floatingtag-pinner/internal/source"
)

func main() {
	fs := flag.NewFlagSet("pinner", flag.ExitOnError)
	daemon := fs.Bool("daemon", false, "keep running: collect every --interval until stopped (default: collect once and exit)")
	kubeconfig := fs.String("kubeconfig", "", "path to kubeconfig (default: in-cluster, then ~/.kube/config)")
	namespace := fs.String("namespace", "", "namespace to watch (default: all)")
	annotation := fs.String("annotation", source.DefaultAnnotation, "annotation that marks target resources")
	onNew := fs.String("on-new", string(collector.OnNewRegister), "for images with no record: register|ignore")
	floating := fs.String("floating-tags", "latest", "comma-separated tags to track; images with other tags are ignored (empty = all tags)")
	localKind := fs.String("local", "node", "local registry: node (container runtime image store of the nodes)")
	pullNS := fs.String("pull-namespace", "default", "namespace for the short-lived Pods that make nodes pull images")
	pullSecret := fs.String("pull-secret", "", "imagePullSecret name (in --pull-namespace) for private registries")
	pullTimeout := fs.Duration("pull-timeout", 2*time.Minute, "per-node timeout for a pull")
	interval := fs.Duration("interval", 5*time.Minute, "with --daemon: time between passes")
	fs.Parse(os.Args[1:])

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
		loc = &local.Node{Client: client, PullNamespace: *pullNS, PullSecret: *pullSecret, PullTimeout: *pullTimeout}
	default:
		fatal(fmt.Errorf("unknown --local %q (supported: node)", *localKind))
	}
	c := &collector.Collector{
		Source:   &source.Kube{Client: client, Namespace: *namespace, Annotation: *annotation, FloatingTags: splitList(*floating)},
		Registry: registry.Remote{},
		Local:    loc,
		OnNew:    policy,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !*daemon {
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
