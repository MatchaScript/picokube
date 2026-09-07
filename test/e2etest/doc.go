// Package e2etest provides the shared helpers used by picokube's
// end-to-end suite under test/e2e. It is not tagged itself — the
// helpers are pure Go (exec wrappers, file assertions, retry loops)
// and individual functions are unit-testable without root.
//
// The consumer package test/e2e is gated by //go:build e2e because
// the suite must run as root on a host that ships kubelet, crictl,
// and CRI-O. Build-tagging only the consumer keeps the helper
// surface usable from future test packages (chaos, performance) and
// from local debugging tools.
package e2etest
