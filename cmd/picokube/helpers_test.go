package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/layouttest"
)

// seedStateAsExisting builds a layouttest layout, writes a kube-apiserver
// static pod manifest (the signal state.Exists() checks for a prior init),
// and returns the layout so callers can pass it to runCmdWithLayout.
func seedStateAsExisting(t *testing.T) layout.Layout {
	t.Helper()
	l := layouttest.New(t)
	manifest := filepath.Join(l.ManifestsDir, "kube-apiserver.yaml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("apiVersion: v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return l
}
