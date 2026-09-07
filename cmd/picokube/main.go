package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"
)

// TODO: switch to k8s.io/component-base/cli.Run + genericclioptions.IOStreams
// once we embed client-go / kubeadm libraries. See
// reference/microshift/cmd/microshift/main.go for the canonical shape:
// cli.Run handles signal forwarding, klog flushing, panic recovery and
// cobra-error → exit-code mapping in one call, and IOStreams lets every
// subcommand take In/Out/ErrOut by injection rather than reaching into
// cmd.OutOrStdout().
func main() {
	// kubeadm config loaders (BytesToInitConfiguration etc.) emit deprecation
	// warnings via klog.Warningf when the input uses an older kubeadm API
	// version. Register klog flags into the default flag set so its writers
	// pick up their initial state, and flush on exit so buffered lines are
	// not lost. klog v2 writes to stderr by default when no log directory is
	// configured, which is what we want for an init-time tool.
	klog.InitFlags(flag.CommandLine)
	defer klog.Flush()

	// SIGTERM handler is required for `picokube boot`, which parks on
	// ctx.Done() after a healthy boot to keep picokube.service active.
	// Other subcommands ignore the cancellation but inherit it for free.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
