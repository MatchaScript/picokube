package preflight_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MatchaScript/picokube/internal/preflight"
)

type stub struct {
	called bool
	err    error
}

func (s *stub) Preflight(ctx context.Context) error {
	s.called = true
	return s.err
}

// Run iterates checks in order and stops at the first failure; later
// checks must not be invoked.
func TestRun_StopsOnFirstError(t *testing.T) {
	first := &stub{}
	bad := &stub{err: errors.New("boom")}
	never := &stub{}

	err := preflight.Run(context.Background(), first, bad, never)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("Run: want boom, got %v", err)
	}
	if !first.called {
		t.Error("first stub was skipped")
	}
	if !bad.called {
		t.Error("failing stub was skipped")
	}
	if never.called {
		t.Error("post-failure stub was invoked")
	}
}

// FSWritable should succeed on a temp dir (writable by definition).
func TestFSWritable_HappyPath(t *testing.T) {
	fs := preflight.FSWritable{Dirs: []string{filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")}}
	if err := fs.Preflight(context.Background()); err != nil {
		t.Fatalf("FSWritable.Preflight on writable temp dirs: %v", err)
	}
}
