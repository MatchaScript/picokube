//go:build e2e

// Package e2e is picokube's end-to-end suite. It drives the full
// bootstrap → boot → workload → reset lifecycle from inside a VM
// booted off the bootc node image (packaging/Containerfile), which
// already carries kubelet, CRI-O, kubectl, crictl, the sysctls and the
// units picokube expects. The only host state the suite provisions is
// /etc/picokube/config.yaml, which depends on the node's address and
// hostname and so cannot live in the image.
//
// The suite is gated by //go:build e2e — `go test ./...` does not see
// it, only `go test -tags e2e ./test/e2e/...` does. It must run as
// root (the binary mutates /etc/kubernetes, /var/lib/etcd, …) and
// will refuse to start otherwise.
//
// It ships as a binary baked into the image rather than as a `go test`
// run: the image has no Go toolchain and no source tree. hack/e2e.sh
// builds the image and runs it:
//
//	go test -c -tags e2e -o e2e.test ./test/e2e   (in the Containerfile)
//	bcvk ephemeral run-ssh --rm <image> -- /usr/libexec/picokube/e2e.test -test.v
//
// `ephemeral run-ssh` is the bcvk mode that propagates the guest
// command's exit status, so the suite's result is the script's result.
//
// Method ordering is load-bearing. Tests are named TestNN_Group_Case
// and run alphabetically by reflect.Type.Method order (testify's
// dispatch rule), which gives us deterministic suite execution
// without manually wiring t.Run subtests. Do not renumber casually;
// later tests rely on cluster state established by earlier ones (a
// Test07 boot test, for instance, assumes Test04 init has run).
//
// Env vars:
//
//	PICOKUBE_E2E_KEEP=1   keep /tmp/picokube-e2e-<pid> after the suite
//	                      (default: kept only on failure)
package e2e
