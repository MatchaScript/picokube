package v1alpha1

import (
	"errors"
	"fmt"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"

	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/version"
)

// Validate runs the picokube-specific gates on the loaded configuration.
// kubeadm's own validator (validation.ValidateInitConfiguration) has
// already run inside BytesToInitConfiguration; this only adds the
// constraints that belong to picokube's deployment model.
//
// Defaults must be applied to wrapper before calling Validate.
func Validate(wrapper *PicoKubeConfig, kubeadmCfg *kubeadmapi.InitConfiguration, l layout.Layout) error {
	var errs []error

	if wrapper.APIVersion != APIVersion {
		errs = append(errs, fmt.Errorf("apiVersion must be %q, got %q", APIVersion, wrapper.APIVersion))
	}
	if wrapper.Kind != Kind {
		errs = append(errs, fmt.Errorf("kind must be %q, got %q", Kind, wrapper.Kind))
	}
	if kubeadmCfg == nil {
		errs = append(errs, errors.New("kubeadm InitConfiguration document is required"))
		return errors.Join(errs...)
	}

	// KubernetesVersion is pinned by the bootc image. Reject any explicit
	// mismatch so an operator does not believe they can roll a single
	// node forward independently of the image.
	if v := kubeadmCfg.ClusterConfiguration.KubernetesVersion; v != "" && v != version.KubernetesVersion {
		errs = append(errs, fmt.Errorf("ClusterConfiguration.kubernetesVersion %q does not match the version pinned in this image (%s); leave it unset to inherit",
			v, version.KubernetesVersion))
	}

	// CertificatesDir lives at a fixed on-disk location on bootc nodes.
	// Reject explicit non-matching values so configuration drift surfaces
	// loudly. (config.Load also normalises this post-validate, so the
	// downstream view is always l.PKIDir.)
	if d := kubeadmCfg.ClusterConfiguration.CertificatesDir; d != "" && d != l.PKIDir {
		errs = append(errs, fmt.Errorf("ClusterConfiguration.certificatesDir %q does not match this image's PKI directory (%s); leave it unset",
			d, l.PKIDir))
	}

	return errors.Join(errs...)
}
