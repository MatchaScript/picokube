//go:build upgrade

// Package upgrade drives the two bootc-level scenarios the in-VM e2e suite
// cannot reach: a minor upgrade across a `bootc switch`, and the greenboot
// rollback that follows a deployment which refuses to boot.
//
// Unlike test/e2e this code runs on the *host*. `bcvk ephemeral` boots the
// container rootfs over virtiofs, so there is no deployment to switch and no
// /run/ostree-booted; these scenarios need `bcvk libvirt run`, which goes
// through `bootc install to-disk`. Every guest step goes through
// `bcvk libvirt ssh`.
//
// Inputs are three images, normally supplied by hack/e2e-upgrade.sh:
//
//	PICOKUBE_UPGRADE_BASE   the base the node image is built on; what bcvk
//	                        installs to disk, because it carries no container
//	                        engine to trip bcvk's installer (see arrange)
//	PICOKUBE_UPGRADE_FROM   previous minor, built from release-1.35
//	PICOKUBE_UPGRADE_TO     current tree
//
// PICOKUBE_E2E_KEEP=1 leaves the VMs behind for inspection.
package upgrade

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	criSocket = "unix:///var/run/crio/crio.sock"
	podSubnet = "10.244.0.0/16"

	backupsDir    = "/var/lib/picokube/backups"
	restoreMarker = backupsDir + "/restore"
	lastEventFile = "/var/lib/picokube/state/last-event"
	kubeconfig    = "KUBECONFIG=/etc/kubernetes/admin.conf"

	// bcvk mounts the host's container storage here itself, through a
	// systemd mount unit injected as an SMBIOS credential, and drops the
	// matching STORAGE_OPTS into /etc/environment.d. The path in bcvk's
	// docs/src/libvirt-run.md (/run/virtiofs-mnt-hoststorage) is stale — no
	// code in bcvk constructs it. switchScript still mounts by hand when the
	// mount is absent, for bcvk builds that only attach the device.
	hostStorage = "/run/host-container-storage"
	storageTag  = "hoststorage"

	// How much of the serial console a give-up prints.
	consoleTailLines = 200
)

// kubernetesVersion pulls the version out of `picokube version`, which
// prints "picokube   kubernetes=v1.36.4 commit=… built=…".
var kubernetesVersion = regexp.MustCompile(`kubernetes=(\S+)`)

// ---------------------------------------------------------------------------
// bcvk / guest plumbing
// ---------------------------------------------------------------------------

// bcvk runs the CLI and returns its stdout. bcvk buffers the guest's stdout
// and prints it only when the remote command succeeded; on failure it
// relays stderr alone and folds the remote exit code into the error text,
// so callers can only treat an error as "non-zero".
func bcvk(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bcvk", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("bcvk %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// sshOne runs a single shell command in the guest. bcvk shell-escapes a
// multi-word command but hands a single argument to the remote shell
// verbatim, so one argv element is what keeps redirections and pipes intact.
func sshOne(t *testing.T, vm, cmd string) (string, error) {
	t.Helper()
	return bcvk(t, "libvirt", "ssh", vm, "--", cmd)
}

// sshTry runs a whole script in the guest and returns its stdout.
//
// The script travels base64-encoded, which sidesteps quoting entirely, and
// its streams are captured to files in the guest: on failure bcvk throws the
// remote stdout away, so a second ssh fetches both streams back for the
// failure message. CI logs are the only debugging surface here.
func sshTry(t *testing.T, vm, script string) (string, error) {
	t.Helper()
	wrapper := "echo " + base64.StdEncoding.EncodeToString([]byte(script)) +
		" | base64 -d > /var/tmp/picokube-step.sh; " +
		"bash -eu /var/tmp/picokube-step.sh > /var/tmp/picokube-step.out 2> /var/tmp/picokube-step.err; rc=$?; " +
		"cat /var/tmp/picokube-step.out; exit $rc"
	out, err := sshOne(t, vm, wrapper)
	if err == nil {
		return out, nil
	}
	logs, _ := sshOne(t, vm,
		"echo '--- stdout'; cat /var/tmp/picokube-step.out; echo '--- stderr'; cat /var/tmp/picokube-step.err")
	return out, fmt.Errorf("%w\n--- script\n%s\n%s", err, script, logs)
}

// ssh is sshTry with the failure turned into a fatal test error.
func ssh(t *testing.T, vm, script string) string {
	t.Helper()
	out, err := sshTry(t, vm, script)
	if err != nil {
		t.Fatalf("guest step failed: %v", err)
	}
	return out
}

// startVM boots image under libvirt and registers its removal. Cleanup goes
// in before the run so a half-created domain is torn down too.
func startVM(t *testing.T, image string) string {
	t.Helper()
	name := fmt.Sprintf("picokube-%s-%d", strings.ToLower(t.Name()), time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() {
		if os.Getenv("PICOKUBE_E2E_KEEP") == "1" {
			t.Logf("PICOKUBE_E2E_KEEP=1: leaving VM %s behind", name)
			return
		}
		if _, err := bcvk(t, "libvirt", "rm", "--stop", "--force", name); err != nil {
			t.Logf("removing VM %s: %v", name, err)
		}
	})

	t.Logf("booting %s as domain %s", image, name)
	start := time.Now()
	// --ssh-wait blocks until sshd answers (bcvk's own budget is 180s).
	// 4G / 2 vCPU matches hack/e2e.sh: the control plane plus the workload
	// pods do not fit in a smaller instance type.
	// --filesystem: fedora-bootc declares no default root filesystem, so
	// `bootc install to-disk` fails with "No root filesystem specified"
	// without it. bcvk's own bind-storage test passes ext4 for the same
	// reason.
	args := []string{"libvirt", "run", "--name", name, "--replace",
		"--bind-storage-ro", "--ssh-wait", "--filesystem", "ext4",
		"--memory", "4G", "--cpus", "2"}
	if help, _ := bcvk(t, "libvirt", "run", "--help"); strings.Contains(help, "--log-dir") {
		if err := os.MkdirAll(consoleDir(name), 0o755); err != nil {
			t.Fatalf("console dir: %v", err)
		}
		args = append(args, "--log-dir=console="+consoleDir(name))
	} else {
		t.Log("this bcvk has no --log-dir; a hang will have no console to show")
	}
	out, err := bcvk(t, append(args, image)...)
	if err != nil {
		t.Fatalf("bcvk libvirt run: %v", err)
	}
	t.Logf("VM %s up in %s: %s", name, time.Since(start).Round(time.Second), strings.TrimSpace(out))
	return name
}

// consoleDir is where bcvk writes console.txt for a domain. Derived from the
// name so no state has to be carried between startVM and the wait helpers.
func consoleDir(vm string) string { return filepath.Join(os.TempDir(), "picokube-console-"+vm) }

// dumpConsole logs the tail of the VM's serial console. It is the only view
// into a guest that has stopped answering ssh, and CI logs are the only
// debugging surface, so every wait that gives up calls this first.
func dumpConsole(t *testing.T, vm string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(consoleDir(vm), "console.txt"))
	if err != nil {
		t.Logf("no console log for %s: %v", vm, err)
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > consoleTailLines {
		lines = lines[len(lines)-consoleTailLines:]
	}
	t.Logf("console tail of %s (%d lines):\n%s", vm, len(lines), strings.Join(lines, "\n"))
}

// guestStateScript prints everything that decides whether greenboot rolls a
// deployment back. show() swallows each command's status so one absent unit or
// missing directory cannot cut the dump short — a non-zero exit here is data,
// not a failure.
const guestStateScript = `
show() { echo "=== $* ==="; "$@" 2>&1 || echo "(exit $?)"; }
show systemctl is-active picokube.service
show systemctl is-failed picokube.service
show systemctl list-jobs
show journalctl -u picokube.service -b --no-pager
show journalctl -u greenboot-healthcheck -b --no-pager
# greenboot writes next-deployment-id from this unit's ExecStop, reading
# bootc status's staged digest. Without it the rollback branch refuses to
# act ("Boot counter exhausted but no next-deployment-id set"), so its
# state and ostree's finalization are what decide whether a rollback can
# happen at all. Neither journal is limited to this boot: the ExecStop that
# matters runs during the shutdown of the boot before.
show systemctl is-active greenboot-set-rollback-trigger.service
show journalctl -u greenboot-set-rollback-trigger --no-pager
show journalctl -u ostree-finalize-staged --no-pager
show grub2-editenv - list
show ls -l /var/lib/picokube/backups/
show bootc status --json
`

// dumpGuestState logs what the guest thinks happened. Best effort in both
// directions: the guest may have gone away, and a dump never decides a result.
func dumpGuestState(t *testing.T, vm, when string) {
	t.Helper()
	out, err := sshTry(t, vm, guestStateScript)
	if err != nil {
		t.Logf("guest state (%s) unavailable: %v", when, err)
		return
	}
	t.Logf("guest state (%s):\n%s", when, out)
}

// bootID identifies the running kernel boot, so a reboot can be waited for
// without racing the sshd that is still up on the way down.
func bootID(t *testing.T, vm string) string {
	t.Helper()
	return strings.TrimSpace(ssh(t, vm, "cat /proc/sys/kernel/random/boot_id"))
}

// reboot asks the guest to reboot. The ssh connection dies underneath the
// command, so bcvk always reports an error; that is the expected shape.
func reboot(t *testing.T, vm string) {
	t.Helper()
	t.Logf("rebooting %s", vm)
	if _, err := sshOne(t, vm, "systemctl reboot"); err != nil {
		t.Logf("reboot dropped the connection as expected: %v", err)
	}
}

// waitNewBoot retries until the guest answers with a boot id other than
// before, tolerating the reconnects of one or more reboots. bcvk already
// polls for 60s inside each attempt.
func waitNewBoot(t *testing.T, vm, before string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for attempt := 1; ; attempt++ {
		out, err := sshOne(t, vm, "cat /proc/sys/kernel/random/boot_id")
		switch {
		case err != nil:
			t.Logf("attempt %d: guest not answering yet (%v)", attempt, err)
		case strings.TrimSpace(out) != before:
			t.Logf("guest rebooted (boot id %s) after %d attempt(s)", strings.TrimSpace(out), attempt)
			return
		default:
			t.Logf("attempt %d: still the pre-reboot boot", attempt)
		}
		if time.Now().After(deadline) {
			dumpConsole(t, vm)
			t.Fatalf("%s did not come back from a reboot within %s", vm, timeout)
		}
		time.Sleep(10 * time.Second)
	}
}

// waitUnitActive polls `systemctl is-active`. On the boot after a rollback
// picokube.service has a restore plus a full reconcile to get through.
func waitUnitActive(t *testing.T, vm, unit string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out, err := sshOne(t, vm, "systemctl is-active "+unit)
		if err == nil && strings.TrimSpace(out) == "active" {
			t.Logf("%s is active", unit)
			return
		}
		if time.Now().After(deadline) {
			dumpConsole(t, vm)
			t.Logf("%s", ssh(t, vm, "systemctl status --no-pager "+unit+" || true\njournalctl -u "+unit+" -b --no-pager"))
			t.Fatalf("%s did not become active within %s", unit, timeout)
		}
		time.Sleep(15 * time.Second)
	}
}

// ---------------------------------------------------------------------------
// bootc status
// ---------------------------------------------------------------------------

// bootcStatus mirrors the fields of `bootc status --json` this test reads.
// Shape taken from bootc's own fixtures (crates/lib/src/fixtures/
// spec-only-booted.yaml): the booted deployment's digest is
// status.booted.image.imageDigest and its reference is
// status.booted.image.image.image.
type bootcStatus struct {
	Status struct {
		Booted struct {
			Image struct {
				Image struct {
					Image string `json:"image"`
				} `json:"image"`
				ImageDigest string `json:"imageDigest"`
			} `json:"image"`
		} `json:"booted"`
	} `json:"status"`
}

func bootedTry(t *testing.T, vm string) (ref, digest string, err error) {
	t.Helper()
	out, err := sshOne(t, vm, "bootc status --json")
	if err != nil {
		return "", "", err
	}
	var st bootcStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return "", "", fmt.Errorf("parse bootc status: %w: %s", err, out)
	}
	return st.Status.Booted.Image.Image.Image, st.Status.Booted.Image.ImageDigest, nil
}

func booted(t *testing.T, vm string) (ref, digest string) {
	t.Helper()
	ref, digest, err := bootedTry(t, vm)
	if err != nil {
		t.Fatalf("read booted deployment: %v", err)
	}
	return ref, digest
}

// waitBootedDigest polls until the guest reports want as its booted digest.
// The rollback scenario reboots several times before greenboot gives up, so
// the guest may be unreachable or on the wrong deployment for a while.
func waitBootedDigest(t *testing.T, vm, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		ref, digest, err := bootedTry(t, vm)
		switch {
		case err != nil:
			t.Logf("waiting for rollback: guest not answering (%v)", err)
		case digest == want:
			t.Logf("booted deployment is now %s (%s)", digest, ref)
			return
		case digest != last:
			t.Logf("waiting for rollback: booted is %s (%s)", digest, ref)
			last = digest
		}
		if time.Now().After(deadline) {
			dumpConsole(t, vm)
			dumpGuestState(t, vm, "rollback timed out")
			t.Fatalf("guest never rolled back to %s within %s (last seen %q)", want, timeout, last)
		}
		time.Sleep(20 * time.Second)
	}
}

// hostDigest is the manifest digest podman records for a locally built
// image — the value bootc reports once the guest boots it out of the
// read-only additional image store.
func hostDigest(t *testing.T, image string) string {
	t.Helper()
	out, err := exec.Command("podman", "image", "inspect", "--format", "{{.Digest}}", image).Output()
	if err != nil {
		t.Fatalf("podman image inspect %s: %v", image, err)
	}
	return strings.TrimSpace(string(out))
}

// guestRef qualifies a bare local tag the way containers-storage needs it.
func guestRef(image string) string {
	if strings.Contains(image, "/") {
		return image
	}
	return "localhost/" + image
}

// ---------------------------------------------------------------------------
// guest scripts
// ---------------------------------------------------------------------------

// nginxProbe fetches the ClusterIP over bash's /dev/tcp, so the image needs
// no curl. The subshell keeps a failed connect from taking the script with
// it: a redirection error on `exec` exits a non-interactive shell.
const nginxProbe = `
probe_nginx() {
    local ip body
    ip=$(kubectl get svc e2e-nginx -o jsonpath='{.spec.clusterIP}')
    test -n "$ip"
    for _ in $(seq 1 30) ; do
        body=$( ( exec 3<>/dev/tcp/"$ip"/80 && printf 'GET / HTTP/1.0\r\n\r\n' >&3 && timeout 10 cat <&3 ) 2>/dev/null || true )
        case "$body" in
            *"Welcome to nginx"*) echo "nginx reachable on $ip" ; return 0 ;;
        esac
        sleep 5
    done
    echo "nginx not reachable on $ip" >&2
    return 1
}
`

// bringUpScript writes the host-dependent config, initialises the cluster
// and parks an nginx Deployment behind a ClusterIP on it.
//
// The config rewrites are the five of test/e2e/suite.go's writeConfig, done
// with sed over the same print-defaults stream. No CNI is installed: the
// node image enables CRI-O's own crio0 bridge (packaging/Containerfile),
// which is already what keeps the node Ready before a CNI DaemonSet lands,
// and a single-node ClusterIP needs nothing more.
//
// /var/log/journal is created so the journal survives the reboots — the
// rollback scenario counts boots with `journalctl --list-boots`.
const bringUpScript = `
set -o pipefail
host=$(tr 'A-Z' 'a-z' < /proc/sys/kernel/hostname)
echo "$host" > /etc/hostname
hostnamectl set-hostname "$host"
echo "hostname pinned to $host"

mkdir -p /var/log/journal
systemctl restart systemd-journald

ip=$(ip -4 -o route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')
test -n "$ip"

mkdir -p /etc/picokube
picokube config print-defaults > /tmp/config.yaml
sed -i \
    -e "s|^  advertiseAddress: .*|  advertiseAddress: $ip|" \
    -e "s|^  taints:[[:space:]]*null$|  taints: []|" \
    -e "s|^  criSocket: .*|  criSocket: ` + criSocket + `|" \
    -e "s|^  name: node$|  name: $host|" \
    -e "s|^networking:$|networking:\n  podSubnet: ` + podSubnet + `|" \
    /tmp/config.yaml
for want in "advertiseAddress: $ip" "taints: []" "criSocket: ` + criSocket + `" "name: $host" "podSubnet: ` + podSubnet + `" ; do
    grep -qF "  $want" /tmp/config.yaml || { echo "config rewrite did not land: $want" >&2 ; exit 1 ; }
done
mv /tmp/config.yaml /etc/picokube/config.yaml
echo "wrote /etc/picokube/config.yaml (advertiseAddress=$ip, name=$host)"

# Recompose /boot/grub2/grub.cfg from this image's bootupd files, which is
# what greenboot's rollback depends on.
#
# greenboot only reaches its rollback branch once GRUB has decremented
# boot_counter to 0, and the snippet that does that ships as
# /usr/lib/bootupd/grub2-static/configs.d/08_greenboot.cfg
# (greenboot-rs.spec:88). bootupd composes grub.cfg from those files once,
# during bootc install, out of whatever image is installing. Here that is
# the base image, which carries no greenboot: bcvk boots the source image as
# its own installer and offers no way to keep crio.service — which the node
# image's multi-user.target upholds — from starting there, so a node image
# cannot be the installer. bootc switch then adds a BLS entry without
# re-running bootupd, so the config keeps the base image's contents.
#
# The composition below is bootupd's own (coreos/bootupd
# src/grubconfigs.rs:62-101, install): the header, grub-static-pre.cfg, then
# each configs.d/*.cfg sorted by name and fenced with ### BEGIN/END ###,
# written with mode 0600 (GRUBCONFIG_FILE_MODE, :27). That version appends no
# grub-static-post.cfg.
cfgdir=/usr/lib/bootupd/grub2-static
boot_was_ro=0
if findmnt -no VFS-OPTIONS /boot | tr ',' '\n' | grep -qx ro ; then
    mount -o remount,rw /boot
    boot_was_ro=1
fi
{
    printf '# Generated by bootupd / do not edit\n\n'
    cat "$cfgdir/grub-static-pre.cfg"
    for f in "$cfgdir"/configs.d/*.cfg ; do
        printf '\n### BEGIN %s ###\n' "$(basename "$f")"
        cat "$f"
        printf '### END %s ###\n' "$(basename "$f")"
    done
} > /boot/grub2/grub.cfg.new
chmod 0600 /boot/grub2/grub.cfg.new
mv /boot/grub2/grub.cfg.new /boot/grub2/grub.cfg
grep '^### BEGIN' /boot/grub2/grub.cfg
grep -q boot_counter /boot/grub2/grub.cfg
# Restoring ro is best effort: something on /boot keeps an open writable
# descriptor and the remount returns EBUSY. fstab mounts it ro again on the
# reboot that follows this step, and greenboot manages its own remount
# around writing grubenv.
[ "$boot_was_ro" = 0 ] || mount -o remount,ro /boot ||
    echo "left /boot rw; fstab restores ro on the next boot"

picokube init
systemctl enable --now picokube.service
systemctl is-active picokube.service

# Whether greenboot judges this node before picokube.service is active
# decides whether the next reboot loops. bcvk 0.17, which
# bootc-ubuntu-setup installs, has no --log-dir, so a looping guest leaves
# no console behind — record the ordering here, while the guest still
# answers.
systemctl show -p After greenboot-healthcheck.service
systemctl show -p After picokube.service
grub2-editenv - list || echo "no grubenv"
journalctl -u greenboot-healthcheck -b --no-pager

# Whether greenboot can ever roll back depends on GRUB decrementing
# boot_counter, which only happens if greenboot's /etc/grub.d/08_greenboot.cfg
# made it into the generated grub config. bootc install sets ostree's
# sysroot.bootloader=none (bootc crates/lib/src/install.rs:232), which skips
# grub2-mkconfig, so print what the installed config actually contains.
ostree config --repo=/sysroot/ostree/repo get sysroot.bootloader || echo "no sysroot.bootloader"
ls /etc/grub.d/
grep -rl boot_counter /boot/grub2/ /boot/efi/EFI/ 2>/dev/null || echo "no boot_counter logic in the installed grub config"

export ` + kubeconfig + `
kubectl create deployment e2e-nginx --image=nginx:alpine
kubectl expose deployment e2e-nginx --port=80 --target-port=80
kubectl wait --for=condition=Available deployment/e2e-nginx --timeout=5m
` + nginxProbe + `
probe_nginx
`

// probeScript re-checks the workload after a reboot.
const probeScript = `
export ` + kubeconfig + `
` + nginxProbe + `
probe_nginx
`

// switchScript mounts the host's container storage when bcvk has not
// already done so, then stages the TO image out of it. %s is the guest
// image reference.
const switchScript = `
mountpoint -q ` + hostStorage + ` || {
    mkdir -p ` + hostStorage + `
    mount -t virtiofs ` + storageTag + ` ` + hostStorage + `
}
ls ` + hostStorage + `
env STORAGE_OPTS=additionalimagestore=` + hostStorage + ` \
    bootc switch --transport containers-storage %s
bootc status
`

// ---------------------------------------------------------------------------
// shared arrangement
// ---------------------------------------------------------------------------

// fromState is what the FROM deployment leaves behind for the assertions
// that follow the switch.
type fromState struct {
	vm      string
	version string // picokube's target Kubernetes version, e.g. v1.35.0
	digest  string
	backup  string // the one backup directory the extra reboot produced
	coreDNS string
}

// arrange installs the base image, switches into FROM, brings a cluster up
// on it, reboots once, and records what the post-switch assertions compare
// against.
//
// FROM cannot be installed directly. bcvk boots the source image as its own
// installer and its install script begins with `rm -rf /var/lib/containers`;
// the node image's multi-user.target upholds crio.service
// (packaging/systemd/10-picokube.conf), so containers-storage has an overlay
// mounted under that path by then and the rm fails with EBUSY. The base
// image the node image is built on carries no container engine, and reaching
// FROM through a `bootc switch` costs one boot and keeps the images under
// test byte-identical to what ships.
//
// The extra reboot is load-bearing. `picokube init` records last-boot.json
// under the *current* boot id, so the first picokube.service run skips the
// snapshot (internal/boot/boot.go:160); the boot after the switch skips it
// too, because the deployment id no longer matches (boot.go:162). Only a
// second boot inside FROM produces a backup — the one the upgrade scenario
// asserts on, and the one the rollback scenario's maybeRestore has to find
// (boot.go:323). nginx goes in before that reboot so it is inside the
// backup and survives the restore.
func arrange(t *testing.T, baseImage, fromImage string) fromState {
	t.Helper()
	vm := startVM(t, baseImage)

	switchTo(t, vm, fromImage)
	before := bootID(t, vm)
	reboot(t, vm)
	waitNewBoot(t, vm, before, 10*time.Minute)

	version := kubernetesVersion.FindStringSubmatch(ssh(t, vm, "picokube version"))[1]
	t.Logf("FROM targets Kubernetes %s", version)

	t.Log("step: bring the cluster up on FROM")
	t.Log(ssh(t, vm, bringUpScript))

	t.Log("step: reboot inside FROM so the next boot snapshots it")
	before = bootID(t, vm)
	reboot(t, vm)
	waitNewBoot(t, vm, before, 10*time.Minute)
	waitUnitActive(t, vm, "picokube.service", 10*time.Minute)
	t.Logf("last-event on FROM: %s", strings.TrimSpace(ssh(t, vm, "cat "+lastEventFile)))
	t.Log(ssh(t, vm, probeScript))

	backups := strings.Fields(ssh(t, vm, "ls -1 "+backupsDir))
	require.Lenf(t, backups, 1, "expected exactly one backup after the second FROM boot, got %v", backups)

	ref, digest := booted(t, vm)
	coreDNS := strings.TrimSpace(ssh(t, vm, kubeconfig+" kubectl -n kube-system get deployment coredns "+
		"-o jsonpath='{.spec.template.spec.containers[0].image}'"))

	t.Logf("FROM: ref=%s digest=%s backup=%s coredns=%s", ref, digest, backups[0], coreDNS)
	return fromState{vm: vm, version: version, digest: digest, backup: backups[0], coreDNS: coreDNS}
}

// switchTo stages the TO image out of the read-only host container store.
func switchTo(t *testing.T, vm, toImage string) {
	t.Helper()
	t.Logf("step: bootc switch to %s", guestRef(toImage))
	t.Log(ssh(t, vm, fmt.Sprintf(switchScript, guestRef(toImage))))
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set; run hack/e2e-upgrade.sh", name)
	}
	return v
}

// requireReadonlyVirtiofs gates both scenarios on the one host capability
// they cannot work around: bcvk's --bind-storage-ro attaches the host's
// container storage as a read-only virtiofs filesystem, and libvirt only
// supports that from 11.0.
func requireReadonlyVirtiofs(t *testing.T) {
	t.Helper()
	out, err := bcvk(t, "libvirt", "status", "--format", "json")
	if err != nil {
		t.Fatalf("bcvk libvirt status: %v", err)
	}
	var st struct {
		Version *struct {
			FullVersion string `json:"full_version"`
		} `json:"version"`
		SupportsReadonlyVirtiofs bool `json:"supports_readonly_virtiofs"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("parse bcvk libvirt status: %v: %s", err, out)
	}
	if !st.SupportsReadonlyVirtiofs {
		version := "unknown"
		if st.Version != nil {
			version = st.Version.FullVersion
		}
		t.Fatalf("libvirt %s does not support read-only virtiofs; bcvk --bind-storage-ro needs libvirt >= 11.0. "+
			"bcvk libvirt status: %s", version, strings.TrimSpace(out))
	}
}

// ---------------------------------------------------------------------------
// scenarios
// ---------------------------------------------------------------------------

// TestUpgrade switches a healthy FROM node to TO and asserts the node came
// back on the new deployment, at the new Kubernetes minor, still serving the
// workload it had before.
func TestUpgrade(t *testing.T) {
	requireReadonlyVirtiofs(t)
	baseImage := requireEnv(t, "PICOKUBE_UPGRADE_BASE")
	fromImage := requireEnv(t, "PICOKUBE_UPGRADE_FROM")
	toImage := requireEnv(t, "PICOKUBE_UPGRADE_TO")

	from := arrange(t, baseImage, fromImage)
	vm := from.vm

	switchTo(t, vm, toImage)
	before := bootID(t, vm)
	reboot(t, vm)
	waitNewBoot(t, vm, before, 10*time.Minute)
	waitUnitActive(t, vm, "picokube.service", 10*time.Minute)

	ref, digest := booted(t, vm)
	t.Logf("booted after the switch: ref=%s digest=%s", ref, digest)
	require.Equal(t, hostDigest(t, toImage), digest, "booted deployment is not the TO image")
	require.Contains(t, ref, strings.TrimPrefix(guestRef(toImage), "localhost/"), "booted image reference")

	toVersion := kubernetesVersion.FindStringSubmatch(ssh(t, vm, "picokube version"))[1]
	t.Logf("TO targets Kubernetes %s", toVersion)
	require.NotEqual(t, from.version, toVersion,
		"FROM and TO target the same minor; the upgrade path would not be exercised")

	event := strings.TrimSpace(ssh(t, vm, "cat "+lastEventFile))
	require.Equal(t, fmt.Sprintf("upgraded %s -> %s", from.version, toVersion), event, "last-event")

	backups := strings.Fields(ssh(t, vm, "ls -1 "+backupsDir))
	require.Containsf(t, backups, from.backup,
		"backup %s of the pre-switch deployment is gone (backups: %v)", from.backup, backups)

	t.Log("step: the control plane is the new minor")
	var kv struct {
		ServerVersion struct {
			Major      string `json:"major"`
			Minor      string `json:"minor"`
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	raw := ssh(t, vm, kubeconfig+" kubectl version -o json")
	require.NoError(t, json.Unmarshal([]byte(raw), &kv), "parse kubectl version: %s", raw)
	minor := strings.SplitN(strings.TrimPrefix(toVersion, "v"), ".", 3)[1]
	t.Logf("serverVersion: %s", kv.ServerVersion.GitVersion)
	require.Equal(t, minor, strings.TrimSuffix(kv.ServerVersion.Minor, "+"), "apiserver minor")

	kubelet := strings.TrimSpace(ssh(t, vm, "kubelet --version"))
	t.Logf("kubelet: %s", kubelet)
	require.Contains(t, kubelet, toVersion, "kubelet version")

	// kubeadm tags kube-proxy with the cluster version. CoreDNS carries
	// whatever constants.CoreDNSVersion that kubeadm pins, so assert it
	// moved rather than hard-coding a version this test would have to chase.
	addons := ssh(t, vm, `export `+kubeconfig+`
echo "kube-proxy=$(kubectl -n kube-system get daemonset kube-proxy -o jsonpath='{.spec.template.spec.containers[0].image}')"
echo "coredns=$(kubectl -n kube-system get deployment coredns -o jsonpath='{.spec.template.spec.containers[0].image}')"`)
	t.Logf("addons:\n%s", addons)
	require.Contains(t, addons, "kube-proxy=registry.k8s.io/kube-proxy:"+toVersion, "kube-proxy image tag")
	require.NotContains(t, addons, "coredns="+from.coreDNS, "CoreDNS image did not move with the upgrade")

	t.Log("step: the workload still answers")
	t.Log(ssh(t, vm, probeScript))

	t.Log("step: greenboot is green this boot")
	waitUnitActive(t, vm, "greenboot-healthcheck.service", 10*time.Minute)
	journal := ssh(t, vm, "journalctl -u greenboot-healthcheck -b --no-pager")
	t.Logf("greenboot journal:\n%s", journal)
	require.NotContains(t, journal, "FAILED", "greenboot reported a failed check")
	require.NotContains(t, journal, "RED", "greenboot reported RED")
}

// TestRollback switches to a TO deployment that cannot read its config —
// the config pins the FROM minor, which FROM accepts and TO rejects
// (internal/apis/bootstrap/v1alpha1/validate.go:36) — and asserts greenboot
// exhausts its boot counter, bootc returns to FROM, and picokube restores
// the backup the red.d hook asked for.
func TestRollback(t *testing.T) {
	requireReadonlyVirtiofs(t)
	baseImage := requireEnv(t, "PICOKUBE_UPGRADE_BASE")
	fromImage := requireEnv(t, "PICOKUBE_UPGRADE_FROM")
	toImage := requireEnv(t, "PICOKUBE_UPGRADE_TO")

	from := arrange(t, baseImage, fromImage)
	vm := from.vm

	t.Log("step: pin the FROM minor in config.yaml so TO refuses to boot")
	// ClusterConfiguration is the last document print-defaults emits, so an
	// appended top-level key lands in it.
	t.Log(ssh(t, vm, fmt.Sprintf(
		"echo 'kubernetesVersion: %s' >> /etc/picokube/config.yaml\ntail -n 5 /etc/picokube/config.yaml",
		from.version)))

	bootsBefore := len(strings.Split(strings.TrimSpace(ssh(t, vm, "journalctl -q --list-boots --no-pager")), "\n"))
	t.Logf("boots recorded before the switch: %d", bootsBefore)

	switchTo(t, vm, toImage)

	// TO is staged but not booted yet. This is the last moment at which the
	// units that have to write next-deployment-id during the coming shutdown
	// can be observed alive, with the staged digest still in bootc status.
	dumpGuestState(t, vm, "TO staged, before the reboot")

	before := bootID(t, vm)
	reboot(t, vm)
	waitNewBoot(t, vm, before, 10*time.Minute)

	// The state as of the first boot on TO, before the wait below can
	// obscure it: whether picokube.service failed, whether greenboot judged
	// it, and what it left in the grub environment.
	dumpGuestState(t, vm, "first boot on TO")

	// greenboot: GREENBOOT_MAX_BOOT_ATTEMPTS=3, each attempt running
	// `picokube healthcheck --timeout=5m` from required.d, then red.d and
	// the rollback reboot.
	t.Log("step: wait for greenboot to give up and bootc to roll back")
	waitBootedDigest(t, vm, from.digest, 25*time.Minute)

	t.Log("step: wait for picokube.service to reconcile the restored data")
	waitUnitActive(t, vm, "picokube.service", 10*time.Minute)

	// What greenboot did on the last boot of TO — counter exhaustion or the
	// forced fallback path — decides how many boots the count below sees.
	t.Logf("greenboot on the previous boot:\n%s",
		ssh(t, vm, "journalctl -u greenboot-healthcheck -b -1 --no-pager || echo '(no previous boot in the journal)'"))

	// maybeRestore writes "restored backup <name>" (internal/boot/boot.go:344),
	// and the tail of the same healthy boot overwrites last-event with
	// "healthy at <version>" (:283). So last-event reports the boot, not the
	// restore; the restore itself is durable only in the journal.
	event := strings.TrimSpace(ssh(t, vm, "cat "+lastEventFile))
	t.Logf("last-event: %s", event)

	restoreLog := ssh(t, vm, "journalctl -u picokube.service -b --no-pager")
	t.Logf("picokube.service on the restored boot:\n%s", restoreLog)
	require.Containsf(t, restoreLog, "restoring backup "+from.backup,
		"the boot after the rollback did not restore the backup taken before the switch")

	require.Equal(t, "absent",
		strings.TrimSpace(ssh(t, vm, "test -e "+restoreMarker+" && echo present || echo absent")),
		"restore marker was not cleared")

	boots := strings.TrimSpace(ssh(t, vm, "journalctl -q --list-boots --no-pager"))
	t.Logf("boots:\n%s", boots)
	require.GreaterOrEqual(t, len(strings.Split(boots, "\n"))-bootsBefore, 3,
		"expected at least 3 boots after the switch (greenboot's retries plus the rollback)")

	t.Log("step: the workload came back with the restored data")
	t.Log(ssh(t, vm, probeScript))
}
