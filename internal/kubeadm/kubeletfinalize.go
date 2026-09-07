package kubeadm

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/MatchaScript/nanokube/internal/layout"
)

// FinalizeKubeletKubeconfig points kubelet.conf at the certificate
// kubelet rotates for itself, mirroring kubeadm's kubelet-finalize
// phase (cmd/phases/init/kubeletfinalize.go). That phase is not exported
// as a function, so nanokube carries its own copy.
//
// certs.Init writes kubelet.conf with the client certificate embedded.
// kubelet treats that embedded cert as a bootstrap credential: on first
// start it requests its own client certificate via the CSR API and
// stores the result in <KubeletDir>/pki/kubelet-client-current.pem. The
// embedded copy still expires after a year, so kubelet.conf must be
// rewritten to reference the rotated file by path — otherwise the next
// kubelet start reads the stale embedded credential.
//
// Reports whether kubelet.conf was rewritten. Idempotent: a second call
// sees the path reference already in place and reports false. A missing
// pem means kubelet has not completed its first CSR yet; that is not an
// error, the caller retries later or leaves it to the next boot.
func FinalizeKubeletKubeconfig(l layout.Layout) (bool, error) {
	pemPath := filepath.Join(l.KubeletDir, "pki", "kubelet-client-current.pem")
	if _, err := os.Stat(pemPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", pemPath, err)
	}

	cfg, err := clientcmd.LoadFromFile(l.KubeletKubeconfig)
	if err != nil {
		return false, fmt.Errorf("load %s: %w", l.KubeletKubeconfig, err)
	}
	kctx, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		return false, fmt.Errorf("%s: current-context %q not in context list", l.KubeletKubeconfig, cfg.CurrentContext)
	}
	info, ok := cfg.AuthInfos[kctx.AuthInfo]
	if !ok {
		return false, fmt.Errorf("%s: no user %q for current-context", l.KubeletKubeconfig, kctx.AuthInfo)
	}

	if info.ClientCertificate == pemPath && info.ClientKey == pemPath &&
		len(info.ClientCertificateData) == 0 && len(info.ClientKeyData) == 0 {
		return false, nil
	}

	info.ClientCertificateData = nil
	info.ClientKeyData = nil
	info.ClientCertificate = pemPath
	info.ClientKey = pemPath

	if err := clientcmd.WriteToFile(*cfg, l.KubeletKubeconfig); err != nil {
		return false, fmt.Errorf("write %s: %w", l.KubeletKubeconfig, err)
	}
	return true, nil
}
