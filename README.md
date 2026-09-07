# picokube

Single-node Kubernetes runtime for bootc-based hosts. picokube wraps
upstream kubeadm phases behind a small CLI (`init`, `config`, …) and
expects the kubelet, CRI, and Kubernetes binaries to be supplied by the
bootc image rather than installed at runtime.

## Build

`packaging/Containerfile` produces the node image the e2e suite runs on: a
Fedora 44 bootc host carrying kubelet, CRI-O, kubectl, the `picokube` binary
and its units, plus the e2e suite as `/usr/libexec/picokube/e2e.test`. It
exists to run `hack/e2e.sh`; what picokube releases is the binary and its
units, one release per Kubernetes minor.

```
podman build -t picokube-node:dev -f packaging/Containerfile .
```

No special podman flags: the image is a plain `dnf install` on top of
`quay.io/fedora/fedora-bootc:44`.

`KUBE_MINOR` (default `v1.35`) pins the pkgs.k8s.io and openSUSE CRI-O repos
through `/etc/dnf/vars/kubever` and `criover`, so it selects the kubelet,
kubectl and CRI-O minor. It must match the `k8s.io/kubernetes` minor in
`go.mod`: the suite exercises the embedded kubeadm against the kubelet
installed here.

Boot it with bcvk:

```
bcvk ephemeral run-ssh --rm picokube-node:dev
```

CRI-O comes up on its own: the image does not enable it, so
`multi-user.target.d/10-picokube.conf` upholds it. kubelet is not upheld and
not enabled — `picokube init` and `picokube boot` start it, and `picokube
reset` stops it.

`greenboot-healthcheck` is enabled, which through its `Also=` also enables
`greenboot-set-rollback-trigger`. Its checks come from `packaging/greenboot/`,
copied to `/etc/greenboot/` — `check/required.d` and `check/wanted.d` are where
greenboot 0.16 looks. On an ephemeral VM there is no cluster and no
`/boot/grub2/grubenv`, so the boot is reported RED and greenboot's reboot is
refused for want of a boot counter.

## Test

```
go test ./...        # unit
hack/e2e.sh          # end to end
```

`hack/e2e.sh` builds the image and runs the baked suite inside an ephemeral
VM, exiting with the suite's status:

```
bcvk ephemeral run-ssh --rm --memory 4G --vcpus 2 picokube-node:dev -- \
    /usr/libexec/picokube/e2e.test -test.v
```

The suite drives init → boot → workload → reset against the real node and
provisions only `/etc/picokube/config.yaml`, which depends on the node's
address and hostname (see `test/e2e/doc.go`). `bcvk ephemeral` boots the
container rootfs directly, so `/run/ostree-booted` is absent and the
ostree-gated backup/restore paths of `picokube boot` are not exercised.

## Configuration

picokube reads a multi-document YAML stream modelled on `kubeadm init
--config`. One `PicoKubeConfig` wrapper document identifies the file as
picokube's; the rest are standard kubeadm documents (`InitConfiguration`,
`ClusterConfiguration`, optionally `KubeletConfiguration`) that picokube
hands directly to kubeadm phases at runtime.

```yaml
apiVersion: bootstrap.picokube.io/v1alpha1
kind: PicoKubeConfig
metadata:
  name: local
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: 10.0.0.1
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
networking:
  serviceSubnet: 10.96.0.0/12
  podSubnet: 10.244.0.0/16
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cgroupDriver: systemd
```

`picokube config print-defaults` emits a complete starter template
suitable for `/etc/picokube/config.yaml`; edit
`localAPIEndpoint.advertiseAddress` to a routable IP before feeding the
file to `picokube init`.

### kubeadm API version support

Parsing of the kubeadm portion goes through kubeadm's own
`BytesToInitConfiguration` helper, so the supported set of
`kubeadm.k8s.io/...` API versions tracks kubeadm itself: typically the
current version (`v1beta4` today) plus one deprecated predecessor
(`v1beta3`), with a `klog.Warningf` on stderr for the deprecated one.
When kubeadm drops support for an older version the corresponding
picokube image will stop accepting configs that still use it; the
warning is the signal to migrate.

### Pinned and overridden fields

A few `ClusterConfiguration` fields are managed by the bootc image rather
than by configuration:

- `kubernetesVersion` — pinned by the image, so leave it unset and
  picokube fills it in. An explicit value must equal the pinned one;
  picokube rejects configs that request a different version.
  `print-defaults` leaves it out for that reason: a config generated on
  one image stays usable after the host is switched to an image carrying
  a different Kubernetes version.
- `certificatesDir` — fixed at `/etc/kubernetes/pki`. picokube rejects
  explicit non-matching values and overrides empty defaults.

`JoinConfiguration` documents are rejected outright until multi-node
support lands.
