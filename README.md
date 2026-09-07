# nanokube

Single-node Kubernetes runtime for bootc-based hosts. nanokube wraps
upstream kubeadm phases behind a small CLI (`init`, `config`, …) and
expects the kubelet, CRI, and Kubernetes binaries to be supplied by the
bootc image rather than installed at runtime.

## Build

`packaging/Containerfile` produces the bootc node image: a Fedora 44 bootc
host carrying kubelet, CRI-O, kubectl, the `nanokube` binary and its units.

```
podman build --cap-add=all --security-opt=label=disable --device /dev/fuse \
    -t coralcoast-node:dev -f packaging/Containerfile .
```

The extra podman flags are what `bootc-base-imagectl build-rootfs` needs to
run `rpm-ostree compose` (bwrap) inside the build container.

Boot it with bcvk:

```
bcvk ephemeral run-ssh --rm coralcoast-node:dev
```

kubelet stays in `activating` until `nanokube init` has written
`/var/lib/kubelet/config.yaml`; CRI-O comes up on its own. Neither unit is
enabled — `multi-user.target.d/10-nanokube.conf` upholds them.

## Configuration

nanokube reads a multi-document YAML stream modelled on `kubeadm init
--config`. One `NanoKubeConfig` wrapper document identifies the file as
nanokube's; the rest are standard kubeadm documents (`InitConfiguration`,
`ClusterConfiguration`, optionally `KubeletConfiguration`) that nanokube
hands directly to kubeadm phases at runtime.

```yaml
apiVersion: bootstrap.nanokube.io/v1alpha1
kind: NanoKubeConfig
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
kubernetesVersion: v1.35.0
networking:
  serviceSubnet: 10.96.0.0/12
  podSubnet: 10.244.0.0/16
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cgroupDriver: systemd
```

`nanokube config print-defaults` emits a complete starter template
suitable for `/etc/nanokube/config.yaml`; edit
`localAPIEndpoint.advertiseAddress` to a routable IP before feeding the
file to `nanokube init`.

### kubeadm API version support

Parsing of the kubeadm portion goes through kubeadm's own
`BytesToInitConfiguration` helper, so the supported set of
`kubeadm.k8s.io/...` API versions tracks kubeadm itself: typically the
current version (`v1beta4` today) plus one deprecated predecessor
(`v1beta3`), with a `klog.Warningf` on stderr for the deprecated one.
When kubeadm drops support for an older version the corresponding
nanokube image will stop accepting configs that still use it; the
warning is the signal to migrate.

### Pinned and overridden fields

A few `ClusterConfiguration` fields are managed by the bootc image rather
than by configuration:

- `kubernetesVersion` — must equal the version pinned in this image (or
  be left unset). nanokube rejects configs that request a different
  version.
- `certificatesDir` — fixed at `/etc/kubernetes/pki`. nanokube rejects
  explicit non-matching values and overrides empty defaults.

`JoinConfiguration` documents are rejected outright until multi-node
support lands.
