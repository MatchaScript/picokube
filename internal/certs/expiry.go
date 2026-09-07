// Portions of this file are derived from MicroShift
// (https://github.com/openshift/microshift), copyright © 2021 MicroShift
// Contributors, licensed under the Apache License, Version 2.0.
// See the NOTICE file at the repository root for details.
package certs

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"

	"github.com/MatchaScript/picokube/internal/layout"
)

func pathExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Rotation thresholds, lifted from MicroShift's certsToRegenerate
// (reference/microshift/pkg/cmd/init.go:580). Certificates whose total
// validity is below shortLivedCutoff (≈5 years) are treated as
// short-lived and rotated when fewer than monthsShort months remain;
// everything else is long-lived and rotates at monthsLong months.
const (
	month            = 30 * 24 * time.Hour
	shortLivedCutoff = 5 * 365 * 24 * time.Hour
	monthsShort      = 7
	monthsLong       = 18
)

// NeedsRotation reports whether a certificate should be rotated now.
// Already-expired certs always rotate.
//
// We deliberately do NOT treat `now < NotBefore` as needing rotation:
// kubeadm backdates issuance by 5 minutes (CertificateBackdate in
// kubeadm constants), so a legitimate not-yet-valid cert is impossible.
// A `now < NotBefore` window therefore implies a wrong clock — silently
// rotating would mask the misconfiguration. Letting the next TLS
// handshake surface the error is the correct failure mode.
func NeedsRotation(c *x509.Certificate) bool {
	now := time.Now()
	if now.After(c.NotAfter) {
		return true
	}
	timeLeft := time.Until(c.NotAfter)
	totalValidity := c.NotAfter.Sub(c.NotBefore)
	if totalValidity < shortLivedCutoff {
		return timeLeft < monthsShort*month
	}
	return timeLeft < monthsLong*month
}

// LeafExpiry is one entry in CheckLeaves' report. NotFound is true when
// the underlying file is absent (e.g. super-admin.conf during a
// reconcile boot); in that case Cert / NotAfter / Remaining are zero
// and the entry must be skipped by renewal logic, not treated as
// expired.
type LeafExpiry struct {
	Path      string
	Cert      *x509.Certificate
	NotAfter  time.Time
	Remaining time.Duration
	NotFound  bool
}

// CAExpiry is the CheckCAs equivalent of LeafExpiry. NotFound is fatal
// for callers: every CA is expected to exist by the time boot runs.
type CAExpiry struct {
	Path      string
	Cert      *x509.Certificate
	NotAfter  time.Time
	Remaining time.Duration
	NotFound  bool
}

// CheckLeaves returns one LeafExpiry per LeafKind, reading the current
// state of layout.PKIDir and layout.KubeconfigDir. Missing leaves are
// reported with NotFound=true rather than as an error so callers can
// skip them without special-casing super-admin.conf and other
// conditionally-present files.
//
// The kubeadm renewal manager is used to look up each leaf's current
// NotAfter (it knows how to parse both PKI .crt files and the
// client-cert PEM embedded inside kubeconfig YAML). The parsed
// *x509.Certificate is also retrieved off disk so callers can feed it
// to needsRotation, which decides short-vs-long-lived from cert
// lifetime rather than a global constant.
func CheckLeaves(cfg *kubeadmapi.InitConfiguration, l layout.Layout) (map[LeafKind]LeafExpiry, error) {
	signer := NewSigner(cfg, l)
	mgr, err := signer.renewalManager()
	if err != nil {
		return nil, err
	}

	out := make(map[LeafKind]LeafExpiry, len(AllLeaves()))
	for _, leaf := range AllLeaves() {
		path := leafPath(l, leaf)
		exists, err := pathExists(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			out[leaf] = LeafExpiry{Path: path, NotFound: true}
			continue
		}
		info, err := mgr.GetCertificateExpirationInfo(string(leaf))
		if err != nil {
			return nil, fmt.Errorf("expiry info for %s: %w", leaf, err)
		}
		cert, err := loadLeafCert(l, leaf)
		if err != nil {
			return nil, err
		}
		out[leaf] = LeafExpiry{
			Path:      path,
			Cert:      cert,
			NotAfter:  info.ExpirationDate,
			Remaining: time.Until(info.ExpirationDate),
		}
	}
	return out, nil
}

// CheckCAs returns one CAExpiry per CAKind, reading ca.crt off disk
// directly. The kubeadm renewal manager does not register CAs, so we
// parse them ourselves.
func CheckCAs(l layout.Layout) (map[CAKind]CAExpiry, error) {
	out := make(map[CAKind]CAExpiry, len(AllCAs()))
	for _, ca := range AllCAs() {
		path := caCertPath(l, ca)
		exists, err := pathExists(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			out[ca] = CAExpiry{Path: path, NotFound: true}
			continue
		}
		cert, err := parseCertFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse CA %s: %w", ca, err)
		}
		out[ca] = CAExpiry{
			Path:      path,
			Cert:      cert,
			NotAfter:  cert.NotAfter,
			Remaining: time.Until(cert.NotAfter),
		}
	}
	return out, nil
}

// caCertPath maps a CAKind to the on-disk ca.crt for that signer.
func caCertPath(l layout.Layout, ca CAKind) string {
	return filepath.Join(l.PKIDir, string(ca)+".crt")
}

// caKeyPath maps a CAKind to the on-disk ca.key for that signer.
func caKeyPath(l layout.Layout, ca CAKind) string {
	return filepath.Join(l.PKIDir, string(ca)+".key")
}

// leafPath maps a LeafKind back to the on-disk file the renewal manager
// reads/writes. PKI certs land under PKIDir (with the etcd/ subfolder
// for etcd-* kinds); kubeconfigs are flat under KubernetesDir.
func leafPath(l layout.Layout, leaf LeafKind) string {
	switch leaf {
	case LeafAPIServer:
		return filepath.Join(l.PKIDir, "apiserver.crt")
	case LeafAPIServerKubeletClient:
		return filepath.Join(l.PKIDir, "apiserver-kubelet-client.crt")
	case LeafAPIServerEtcdClient:
		return filepath.Join(l.PKIDir, "apiserver-etcd-client.crt")
	case LeafFrontProxyClient:
		return filepath.Join(l.PKIDir, "front-proxy-client.crt")
	case LeafEtcdServer:
		return filepath.Join(l.PKIDir, "etcd", "server.crt")
	case LeafEtcdPeer:
		return filepath.Join(l.PKIDir, "etcd", "peer.crt")
	case LeafEtcdHealthcheckClient:
		return filepath.Join(l.PKIDir, "etcd", "healthcheck-client.crt")
	case LeafAdminConf, LeafSuperAdminConf, LeafControllerManagerConf, LeafSchedulerConf:
		return filepath.Join(l.KubernetesDir, string(leaf))
	}
	panic("unhandled LeafKind: " + string(leaf))
}

// leafKeyPath returns the on-disk .key partner of a PKI leaf. Kubeconfig
// leaves embed their private key inside the YAML and have no separate
// file — those return "".
func leafKeyPath(l layout.Layout, leaf LeafKind) string {
	switch leaf {
	case LeafAPIServer:
		return filepath.Join(l.PKIDir, "apiserver.key")
	case LeafAPIServerKubeletClient:
		return filepath.Join(l.PKIDir, "apiserver-kubelet-client.key")
	case LeafAPIServerEtcdClient:
		return filepath.Join(l.PKIDir, "apiserver-etcd-client.key")
	case LeafFrontProxyClient:
		return filepath.Join(l.PKIDir, "front-proxy-client.key")
	case LeafEtcdServer:
		return filepath.Join(l.PKIDir, "etcd", "server.key")
	case LeafEtcdPeer:
		return filepath.Join(l.PKIDir, "etcd", "peer.key")
	case LeafEtcdHealthcheckClient:
		return filepath.Join(l.PKIDir, "etcd", "healthcheck-client.key")
	}
	return ""
}

// loadLeafCert parses the *x509.Certificate at the leaf's on-disk path.
// PKI .crt files are PEM-encoded certificates directly; kubeconfig
// leaves embed the client cert PEM under users[].user.client-certificate-data,
// and we extract that via clientcmd.
func loadLeafCert(l layout.Layout, leaf LeafKind) (*x509.Certificate, error) {
	path := leafPath(l, leaf)
	switch leaf {
	case LeafAdminConf, LeafSuperAdminConf, LeafControllerManagerConf, LeafSchedulerConf:
		return parseKubeconfigClientCert(path)
	default:
		return parseCertFile(path)
	}
}

// parseCertFile reads a PEM-encoded x509 cert from disk. Only the first
// CERTIFICATE block is returned — for CAs we want the leaf-issuing cert,
// which is always the first (and only) block in picokube's files.
func parseCertFile(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// parseKubeconfigClientCert pulls the embedded client certificate out
// of a kubeconfig file. kubeadm writes exactly one AuthInfo per
// kubeconfig with the client cert PEM under
// client-certificate-data, so we take the first AuthInfo we see.
func parseKubeconfigClientCert(path string) (*x509.Certificate, error) {
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig %s: %w", path, err)
	}
	for _, ai := range cfg.AuthInfos {
		if len(ai.ClientCertificateData) == 0 {
			continue
		}
		block, _ := pem.Decode(ai.ClientCertificateData)
		if block == nil {
			return nil, fmt.Errorf("no PEM block in client-certificate-data of %s", path)
		}
		return x509.ParseCertificate(block.Bytes)
	}
	return nil, fmt.Errorf("no AuthInfo with client-certificate-data in %s", path)
}
