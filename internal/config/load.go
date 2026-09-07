// Package config loads a picokube configuration file from disk and
// returns it as kubeadm's internal *InitConfiguration. The on-disk
// format is a multi-document YAML stream: one PicoKubeConfig wrapper
// document plus the standard kubeadm InitConfiguration / Cluster
// Configuration / KubeletConfiguration documents that picokube hands
// straight to kubeadm phases at runtime.
//
// Parsing of the kubeadm portion is delegated to kubeadm's own
// BytesToInitConfiguration helper. That gives picokube the upstream
// defaulter, the upstream validator, and — critically — kubeadm's
// deprecation warning path for older API versions (klog.Warningf
// emitted by validateSupportedVersion) for free.
package config

import (
	"bytes"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmapiv1 "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1beta4"
	kubeadmutil "k8s.io/kubernetes/cmd/kubeadm/app/util"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/MatchaScript/picokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/picokube/internal/layout"
)

// Load reads the multi-document YAML at path, parses both the
// PicoKubeConfig wrapper and the sibling kubeadm documents, applies
// defaults, validates, and returns the upstream kubeadm internal
// InitConfiguration that downstream packages consume directly.
func Load(path string, l layout.Layout) (*kubeadmapi.InitConfiguration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parse(data, path, l)
}

// LoadDefault reads the canonical config file for the given layout.
func LoadDefault(l layout.Layout) (*kubeadmapi.InitConfiguration, error) {
	return Load(l.ConfigFile, l)
}

func parse(data []byte, source string, l layout.Layout) (*kubeadmapi.InitConfiguration, error) {
	gvkmap, err := kubeadmutil.SplitConfigDocuments(data)
	if err != nil {
		return nil, fmt.Errorf("split %s: %w", source, err)
	}

	// JoinConfiguration is unimplemented; reject early with a clear
	// error rather than letting kubeadm parse a document we will never
	// act on.
	for gvk := range gvkmap {
		if gvk.Group == kubeadmapiv1.SchemeGroupVersion.Group && gvk.Kind == "JoinConfiguration" {
			return nil, fmt.Errorf("parse %s: JoinConfiguration is not supported yet; multi-node bootstrap is on the roadmap", source)
		}
	}

	// Pull the picokube wrapper out of the map before handing the rest
	// to kubeadm. Otherwise kubeadm would emit an "Ignored configuration
	// document" klog warning for our wrapper's GVK.
	wrapper, err := extractWrapper(gvkmap, source)
	if err != nil {
		return nil, err
	}
	v1alpha1.SetDefaults(wrapper)

	kubeadmBytes := concatDocs(gvkmap)
	kubeadmCfg, err := kubeadmconfig.BytesToInitConfiguration(kubeadmBytes, false)
	if err != nil {
		return nil, fmt.Errorf("parse kubeadm portion of %s: %w", source, err)
	}

	// kubeadm's defaulter fills CertificatesDir with its own canonical
	// path even when the user left it unset. Restore the empty-string
	// sentinel before validating so Validate only sees values the user
	// explicitly wrote — the empty-string branch is "OK", and we pin
	// l.PKIDir unconditionally afterward.
	if kubeadmCfg.CertificatesDir == kubeadmapiv1.DefaultCertificatesDir {
		kubeadmCfg.CertificatesDir = ""
	}

	if err := v1alpha1.Validate(wrapper, kubeadmCfg, l); err != nil {
		return nil, fmt.Errorf("validate %s: %w", source, err)
	}

	// Pin the on-disk PKI location regardless of whether the user
	// supplied a CertificatesDir. Validate has rejected an explicit
	// non-matching value, so the only mismatch left is the empty default
	// kubeadm filled in ("/etc/kubernetes/pki", which happens to equal
	// paths.PKIDir today) — normalising here keeps every downstream
	// reader on a single canonical path even if paths.PKIDir is
	// retargeted in the future.
	kubeadmCfg.CertificatesDir = l.PKIDir
	return kubeadmCfg, nil
}

func extractWrapper(gvkmap kubeadmapi.DocumentMap, source string) (*v1alpha1.PicoKubeConfig, error) {
	pkGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupName,
		Version: v1alpha1.Version,
		Kind:    v1alpha1.Kind,
	}
	raw, ok := gvkmap[pkGVK]
	if !ok {
		return nil, fmt.Errorf("parse %s: required document %s (kind=%s) not found", source, v1alpha1.APIVersion, v1alpha1.Kind)
	}
	delete(gvkmap, pkGVK)

	pk := &v1alpha1.PicoKubeConfig{}
	if err := yaml.UnmarshalStrict(raw, pk); err != nil {
		return nil, fmt.Errorf("parse %s PicoKubeConfig: %w", source, err)
	}
	return pk, nil
}

func concatDocs(gvkmap kubeadmapi.DocumentMap) []byte {
	var buf bytes.Buffer
	first := true
	for _, b := range gvkmap {
		if !first {
			buf.WriteString("---\n")
		}
		buf.Write(b)
		if len(b) == 0 || b[len(b)-1] != '\n' {
			buf.WriteByte('\n')
		}
		first = false
	}
	return buf.Bytes()
}

// Marshal serialises a PicoKubeConfig wrapper plus a kubeadm
// InitConfiguration as a multi-document YAML stream. The wrapper is
// emitted via sigs.k8s.io/yaml; the kubeadm portion goes through
// kubeadm's own MarshalInitConfigurationToBytes (which emits Init- and
// ClusterConfiguration as separate documents and handles TypeMeta
// inlining correctly).
//
// Used by `picokube config print-defaults`. kubeadmCfg may be nil; in
// that case only the wrapper is emitted.
func Marshal(wrapper *v1alpha1.PicoKubeConfig, kubeadmCfg *kubeadmapi.InitConfiguration) ([]byte, error) {
	wrapperBytes, err := yaml.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal wrapper: %w", err)
	}
	if kubeadmCfg == nil {
		return wrapperBytes, nil
	}
	kubeadmBytes, err := kubeadmconfig.MarshalInitConfigurationToBytes(kubeadmCfg, kubeadmapiv1.SchemeGroupVersion)
	if err != nil {
		return nil, fmt.Errorf("marshal kubeadm portion: %w", err)
	}

	var buf bytes.Buffer
	buf.Write(wrapperBytes)
	if len(wrapperBytes) == 0 || wrapperBytes[len(wrapperBytes)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("---\n")
	buf.Write(kubeadmBytes)
	return buf.Bytes(), nil
}
