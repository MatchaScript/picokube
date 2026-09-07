//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/MatchaScript/picokube/test/e2etest"
)

const (
	binPath    = "/usr/bin/picokube"
	kubeconfig = "/etc/kubernetes/admin.conf"
	configPath = "/etc/picokube/config.yaml"
	criSocket  = "unix:///var/run/crio/crio.sock"
	podSubnet  = "10.244.0.0/16"
	// Pinned: `latest` would silently retarget the data-plane test at a
	// release nobody has run here. v0.28.9 is flannel-io/flannel's latest
	// tag (GitHub releases API, checked 2026-09-07).
	flannelURL = "https://github.com/flannel-io/flannel/releases/download/v0.28.9/kube-flannel.yml"
)

// PicokubeE2ESuite drives the full bootstrap → boot → workload → reset
// lifecycle on the bootc node image, from inside the VM. State carries
// between tests; methods are named TestNN_Group_Case so testify's
// lexicographic dispatch order preserves the bash suite's ordering.
type PicokubeE2ESuite struct {
	suite.Suite

	binPath    string
	kubeconfig string
	nodeName   string

	dumpRoot   string
	currentDir string
	testStart  time.Time

	keepArtifacts bool

	H *e2etest.Helpers
}

// SetupSuite runs once at the start: root check, env, paths, and the
// host state that is not image content — /etc/picokube/config.yaml and
// the absence of a previous run's artefacts.
func (s *PicokubeE2ESuite) SetupSuite() {
	if os.Geteuid() != 0 {
		s.T().Fatal("e2e suite must run as root (it is meant to run as /usr/libexec/picokube/e2e.test inside the node image; see hack/e2e.sh)")
	}

	s.binPath = binPath
	s.kubeconfig = kubeconfig
	s.T().Setenv("KUBECONFIG", s.kubeconfig)

	s.nodeName = s.pinHostname()

	s.keepArtifacts = os.Getenv("PICOKUBE_E2E_KEEP") == "1"

	s.dumpRoot = filepath.Join(os.TempDir(), fmt.Sprintf("picokube-e2e-%d", os.Getpid()))
	s.Require().NoError(os.MkdirAll(s.dumpRoot, 0o755))
	s.T().Logf("dump root: %s", s.dumpRoot)

	// H is rebound to the per-test t in SetupTest; the suite-level
	// binding is what writeConfig and cleanLeftovers report through.
	s.H = s.newHelpers()

	s.writeConfig()
	s.cleanLeftovers()
}

// pinHostname makes the node name immovable for the rest of the run and
// returns it.
//
// The image ships no /etc/hostname, so the hostname is transient and
// NetworkManager replaces it with whatever DHCP or a reverse lookup of the
// leased address yields — which can land minutes into the run. kubelet takes
// its node name from the hostname, so a rename after `picokube init` makes it
// re-register under a new name, and every authorization on
// system:node:<name> then fails ("node 'x' cannot read 'y'"). A static
// hostname takes precedence over NetworkManager's, so write one before init.
func (s *PicokubeE2ESuite) pinHostname() string {
	host, err := os.Hostname()
	s.Require().NoError(err, "os.Hostname")
	name := strings.ToLower(host)
	s.Require().NoError(os.WriteFile("/etc/hostname", []byte(name+"\n"), 0o644),
		"pin static hostname")
	s.Require().NoError(syscall.Sethostname([]byte(name)), "apply hostname")
	s.T().Logf("hostname pinned to %s", name)
	return name
}

// writeConfig seeds /etc/picokube/config.yaml from `picokube config
// print-defaults` and overrides the fields that depend on this host, so
// the image itself can stay host-independent:
//
//   - localAPIEndpoint.advertiseAddress: this host's routable address, so
//     the apiserver SAN matches.
//   - nodeRegistration.taints: an explicit empty list, so the lone
//     control-plane node is schedulable for the workload connectivity
//     test. `taints: null` means "use the default control-plane taint".
//   - nodeRegistration.criSocket: CRI-O. Left unset, kubeadm autodetects
//     and trips on any second CRI endpoint.
//   - nodeRegistration.name: this host's lowercased hostname, matching
//     what the assertions look up (pod/etcd-<hostname>). The
//     print-defaults stub is `name: node`.
//   - networking.podSubnet: kubeadm's default omits it, which leaves the
//     flannel of Test11 crash-looping on "failed to acquire lease".
//
// Every InitConfiguration field touched here lives at 2-space indent in
// the multi-document stream print-defaults emits. A rewrite that does not
// land fails the suite here rather than as a confusing downstream error.
func (s *PicokubeE2ESuite) writeConfig() {
	ip := s.routableIP()
	out, _ := s.H.Picokube("config", "print-defaults")

	for _, r := range []struct{ pattern, repl, want string }{
		{`(?m)^(  advertiseAddress: ).*$`, "${1}" + ip, "advertiseAddress: " + ip},
		{`(?m)^(  taints:)\s+null$`, "${1} []", "taints: []"},
		{`(?m)^(  criSocket: ).*$`, "${1}" + criSocket, "criSocket: " + criSocket},
		{`(?m)^(  name: )node$`, "${1}" + s.nodeName, "name: " + s.nodeName},
		{`(?m)^networking:$`, "networking:\n  podSubnet: " + podSubnet, "podSubnet: " + podSubnet},
	} {
		out = regexp.MustCompile(r.pattern).ReplaceAllString(out, r.repl)
		s.Require().Containsf(out, r.want, "config rewrite %s did not land", r.pattern)
	}

	s.Require().NoError(os.MkdirAll(filepath.Dir(configPath), 0o755))
	s.Require().NoError(os.WriteFile(configPath, []byte(out), 0o644))
	s.T().Logf("wrote %s (advertiseAddress=%s, name=%s)", configPath, ip, s.nodeName)
}

// routableIP returns the source address the kernel picks for off-link
// traffic — the equivalent of `hostname -I | awk '{print $1}'` without
// depending on which interface the VM came up on. The UDP socket is
// never written to, so nothing leaves the host.
func (s *PicokubeE2ESuite) routableIP() string {
	c, err := net.Dial("udp", "1.1.1.1:80")
	s.Require().NoError(err, "pick routable source address")
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// cleanLeftovers removes the artefacts of a previous run so the suite can
// be re-run against a long-lived debug VM. A fresh ephemeral VM has none
// of these; reset is best-effort for the same reason.
func (s *PicokubeE2ESuite) cleanLeftovers() {
	if _, err := os.Stat("/etc/kubernetes"); err == nil {
		s.T().Log("previous run detected; resetting")
		_, _, _ = s.H.PicokubeRaw("reset", "--yes")
	}
	for _, p := range []string{"/etc/kubernetes", "/var/lib/etcd", "/var/lib/kubelet", "/var/lib/picokube"} {
		s.Require().NoError(os.RemoveAll(p))
	}
}

func (s *PicokubeE2ESuite) newHelpers() *e2etest.Helpers {
	return e2etest.New(s.T(), e2etest.Config{
		Bin:        s.binPath,
		Kubeconfig: s.kubeconfig,
		NodeName:   s.nodeName,
		FlannelURL: flannelURL,
	})
}

// TearDownSuite removes the dump root unless a test failed or
// PICOKUBE_E2E_KEEP=1 asked for preservation. picokube reset is NOT
// called here — the final test (Test16) exercises reset itself, and
// teardown-time reset would mask reset-path failures.
func (s *PicokubeE2ESuite) TearDownSuite() {
	if s.T().Failed() || s.keepArtifacts {
		s.T().Logf("preserving dump root: %s", s.dumpRoot)
		return
	}
	if err := os.RemoveAll(s.dumpRoot); err != nil {
		s.T().Logf("remove dump root %s: %v", s.dumpRoot, err)
	}
}

// SetupTest sets up the per-test diagnostic subdirectory and rebinds
// the helpers to the per-test *testing.T. The rebind is critical:
// testify swaps s.T() for each test method, but a helper captured in
// SetupSuite would keep the suite-level t and any t.Fatalf call from
// inside a test would FailNow the parent suite goroutine (printing
// "subtest may have called FailNow on a parent test") instead of the
// test, suppressing TearDownTest diagnostics.
//
// State is NOT reset between tests — the bash suite carries state
// through, so the Go port follows the same model. TearDownTest
// collects diagnostics only when a test fails.
func (s *PicokubeE2ESuite) SetupTest() {
	s.currentDir = filepath.Join(s.dumpRoot, s.T().Name())
	if err := os.MkdirAll(s.currentDir, 0o755); err != nil {
		s.T().Logf("setup: mkdir %s: %v", s.currentDir, err)
	}
	s.testStart = time.Now()
	s.H = s.newHelpers()
}

// TearDownTest dumps diagnostics on failure and logs test duration.
func (s *PicokubeE2ESuite) TearDownTest() {
	if s.T().Failed() {
		s.H.DumpDiagnostics(s.currentDir)
		s.T().Logf("artifacts: %s", s.currentDir)
	}
	s.T().Logf("test %q done in %s", s.T().Name(), time.Since(s.testStart))
}
