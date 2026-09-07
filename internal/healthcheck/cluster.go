package healthcheck

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// WaitForControlPlane polls in parallel for four conditions to all hold:
//
//   - Node <nodeName> reports Ready=True
//   - Static pod kube-apiserver-<nodeName> reports Ready=True
//   - Static pod kube-controller-manager-<nodeName> reports Ready=True
//   - Static pod kube-scheduler-<nodeName> reports Ready=True
//
// Mirrors kinder's waitNewControlPlaneNodeReady
// (reference/kubeadm/kinder/pkg/cluster/manager/actions/waiter.go).
// Returns nil when all four pass; returns ctx.Err() (wrapped) on timeout.
// Requires an authenticated client.
func WaitForControlPlane(ctx context.Context, client kubernetes.Interface, nodeName string) error {
	checks := []readyCheck{
		{name: fmt.Sprintf("node/%s", nodeName), fn: func(ctx context.Context) bool { return nodeReady(ctx, client, nodeName) }},
		{name: fmt.Sprintf("pod/kube-apiserver-%s", nodeName), fn: func(ctx context.Context) bool {
			return staticPodReady(ctx, client, "kube-apiserver-"+nodeName)
		}},
		{name: fmt.Sprintf("pod/kube-controller-manager-%s", nodeName), fn: func(ctx context.Context) bool {
			return staticPodReady(ctx, client, "kube-controller-manager-"+nodeName)
		}},
		{name: fmt.Sprintf("pod/kube-scheduler-%s", nodeName), fn: func(ctx context.Context) bool {
			return staticPodReady(ctx, client, "kube-scheduler-"+nodeName)
		}},
	}
	return waitAllReady(ctx, checks)
}

type readyCheck struct {
	name string
	fn   func(context.Context) bool
}

func waitAllReady(ctx context.Context, checks []readyCheck) error {
	// CR5: derive a cancellable child so success cancels every still-running
	// per-check goroutine. Without this, a caller that succeeded (e.g.
	// `picokube healthcheck` CLI) would leave goroutines polling the
	// apiserver until process exit. Boot was usually saved by SIGTERM,
	// but the CLI's one-shot exit window leaked all four goroutines.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	passed := make(chan string, len(checks))
	for _, c := range checks {
		c := c
		go func() {
			for {
				if c.fn(ctx) {
					passed <- c.name
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		}()
	}
	remaining := len(checks)
	for remaining > 0 {
		select {
		case <-passed:
			remaining--
		case <-ctx.Done():
			return fmt.Errorf("control plane not ready: %w (%d/%d checks still pending)",
				ctx.Err(), remaining, len(checks))
		}
	}
	return nil
}

func nodeReady(ctx context.Context, client kubernetes.Interface, nodeName string) bool {
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func staticPodReady(ctx context.Context, client kubernetes.Interface, podName string) bool {
	pod, err := client.CoreV1().Pods(metav1.NamespaceSystem).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
