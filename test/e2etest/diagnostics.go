package e2etest

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DumpDiagnostics writes a directory of diagnostic files under outDir
// covering the same surface as test/e2e/lib.sh's dump_diagnostics.
// Each source becomes a separate file so artifacts are greppable.
//
// Best-effort: failures are logged via h.t.Logf rather than fatal so
// diagnostics collection itself never masks the original test failure.
func (h *Helpers) DumpDiagnostics(outDir string) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		h.t.Logf("dump: mkdir %s: %v", outDir, err)
		return
	}

	for _, u := range []string{"crio.service", "kubelet.service", "picokube.service"} {
		short := strings.TrimSuffix(u, ".service")
		h.captureCmd(filepath.Join(outDir, "systemctl-status-"+short+".txt"),
			"systemctl", "status", "--no-pager", "--full", u)
		h.captureCmd(filepath.Join(outDir, "journal-"+short+".log"),
			"journalctl", "--no-pager", "-u", u, "-n", "200")
	}

	h.captureFile(filepath.Join(outDir, "picokube-last-event.txt"),
		"/var/lib/picokube/state/last-event")
	h.captureFile(filepath.Join(outDir, "picokube-last-boot.json"),
		"/var/lib/picokube/state/last-boot.json")

	if _, err := os.Stat(h.kubeconfig); err == nil {
		h.captureCmd(filepath.Join(outDir, "kubectl-get-nodes.txt"),
			"kubectl", "--kubeconfig", h.kubeconfig, "get", "nodes", "-o", "wide")
		h.captureCmd(filepath.Join(outDir, "kubectl-get-pods-all.txt"),
			"kubectl", "--kubeconfig", h.kubeconfig, "get", "pods", "-A", "-o", "wide")
		h.captureCmd(filepath.Join(outDir, "kubectl-get-events.txt"),
			"kubectl", "--kubeconfig", h.kubeconfig, "get", "events", "-A",
			"--sort-by=.lastTimestamp")
	}

	if _, err := exec.LookPath("crictl"); err == nil {
		h.captureCmd(filepath.Join(outDir, "crictl-ps-a.txt"), "crictl", "ps", "-a")
	}

	if _, err := os.Stat("/etc/kubernetes/manifests"); err == nil {
		h.captureCmd(filepath.Join(outDir, "manifests-listing.txt"),
			"ls", "-la", "/etc/kubernetes/manifests")
	}
}

func (h *Helpers) captureCmd(path, name string, args ...string) {
	f, err := os.Create(path)
	if err != nil {
		h.t.Logf("dump: create %s: %v", path, err)
		return
	}
	defer f.Close()
	cmd := exec.Command(name, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(f, "\n(command failed: %v)\n", err)
	}
}

func (h *Helpers) captureFile(dst, src string) {
	in, err := os.Open(src)
	if err != nil {
		_ = os.WriteFile(dst, []byte(fmt.Sprintf("(absent: %v)\n", err)), 0o644)
		return
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		h.t.Logf("dump: create %s: %v", dst, err)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		h.t.Logf("dump: copy %s -> %s: %v", src, dst, err)
	}
}
