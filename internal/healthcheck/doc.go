// Package healthcheck answers two layers of "is this picokube cluster
// healthy?" against the live API:
//
//	apiserver.go   apiserver endpoint: /readyz probe used to wait out
//	               kube-apiserver's static-pod startup window.
//	cluster.go     cluster resources: Node Ready + the three control-
//	               plane static pods Ready, which `picokube healthcheck`
//	               + boot.Run + initialize.Run all gate on.
//
// Pre-action gates (writability probes, free-space checks) live in
// package preflight instead — separating "is the host ready to act"
// from "is the cluster running healthily".
package healthcheck
