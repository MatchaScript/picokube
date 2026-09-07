package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatchaScript/picokube/internal/layouttest"
)

func TestReadLastBoot_MissingFileIsNotAnError(t *testing.T) {
	l := layouttest.New(t)

	lb, had, err := ReadLastBoot(l)
	if err != nil {
		t.Fatalf("ReadLastBoot on empty state: %v", err)
	}
	if had {
		t.Errorf("had = true on empty state")
	}
	if lb != (LastBoot{}) {
		t.Errorf("LastBoot = %+v; want zero value", lb)
	}
}

func TestWriteLastBoot_RoundTripPreservesFields(t *testing.T) {
	l := layouttest.New(t)

	want := LastBoot{
		Version:      "v1.35.0",
		DeploymentID: "abc123def456",
		BootID:       "11112222-3333-4444-5555-666677778888",
	}
	if err := WriteLastBoot(l, want); err != nil {
		t.Fatalf("WriteLastBoot: %v", err)
	}

	got, had, err := ReadLastBoot(l)
	if err != nil || !had {
		t.Fatalf("ReadLastBoot: err=%v had=%v", err, had)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestWriteLastBoot_CreatesStateDir(t *testing.T) {
	l := layouttest.New(t)
	if _, err := os.Stat(l.StateDir); !os.IsNotExist(err) {
		t.Fatalf("pre-condition: StateDir must not exist yet; got %v", err)
	}
	if err := WriteLastBoot(l, LastBoot{Version: "v1.0"}); err != nil {
		t.Fatalf("WriteLastBoot: %v", err)
	}
	if _, err := os.Stat(l.StateDir); err != nil {
		t.Errorf("StateDir not created: %v", err)
	}
}

func TestWriteLastBoot_IsAtomic(t *testing.T) {
	l := layouttest.New(t)

	if err := WriteLastBoot(l, LastBoot{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLastBoot(l, LastBoot{Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadLastBoot(l)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v2" {
		t.Errorf("last write lost: got %q", got.Version)
	}
	// No stray .tmp-* leftover in StateDir.
	entries, err := os.ReadDir(l.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestReadLastBoot_CorruptedFileBubbles(t *testing.T) {
	l := layouttest.New(t)
	if err := os.MkdirAll(l.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.LastBootFile, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadLastBoot(l)
	if err == nil {
		t.Fatal("ReadLastBoot on corrupt file = nil")
	}
}

func TestWriteLastEvent_AppendsNewline(t *testing.T) {
	l := layouttest.New(t)

	if err := WriteLastEvent(l, "hello world"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(l.LastEventFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello world\n" {
		t.Errorf("event file = %q; want %q", string(raw), "hello world\n")
	}

	got, err := ReadLastEvent(l)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("ReadLastEvent trimmed to %q", got)
	}
}

func TestReadLastEvent_EmptyWhenAbsent(t *testing.T) {
	l := layouttest.New(t)
	got, err := ReadLastEvent(l)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("ReadLastEvent on empty state = %q; want empty", got)
	}
}

// Exists() trips on either the kube-apiserver static pod manifest
// (init done) OR the /var/lib/picokube tree (lifecycle data left over).
// Cover each independently so a future refactor dropping one fails the
// matching subtest.
func TestExists_TwoIndependentSignals(t *testing.T) {
	t.Run("nothing exists", func(t *testing.T) {
		l := layouttest.New(t)
		got, err := Exists(l)
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Fatal("Exists() = true on empty state")
		}
	})

	t.Run("manifest alone trips", func(t *testing.T) {
		l := layouttest.New(t)
		manifest := filepath.Join(l.ManifestsDir, "kube-apiserver.yaml")
		if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, []byte("apiVersion: v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Exists(l)
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Fatal("Exists() = false; manifest alone must trip")
		}
	})

	t.Run("var-lib-picokube alone trips", func(t *testing.T) {
		l := layouttest.New(t)
		// Anything under PicoKubeVarDir creates the dir; last-boot.json
		// is the realistic case (lifecycle wrote it then someone wiped
		// /etc/kubernetes manually).
		if err := WriteLastBoot(l, LastBoot{Version: "v1"}); err != nil {
			t.Fatal(err)
		}
		got, err := Exists(l)
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Fatal("Exists() = false; /var/lib/picokube alone must trip")
		}
	})
}
