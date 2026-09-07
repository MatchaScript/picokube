// Package state manages the small metadata files under
// /var/lib/picokube/state/ that describe the most recent successful boot.
//
// Two files matter:
//
//   - last-boot.json: JSON metadata for the boot that last completed
//     successfully. Holds the picokube version, the ostree/bootc
//     deployment id (when applicable) and the kernel boot id. Used at
//     the start of the next boot to detect upgrades and to name the
//     backup of the data produced by that previous boot.
//   - last-event: human-readable one-liner describing the most recent
//     lifecycle event. Surfaced via greenboot wanted.d to MOTD.
//
// Rollback is triggered by an external marker file placed by the
// greenboot red.d hook; that logic lives in the backup package. No
// state file tracks rollback intent.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MatchaScript/picokube/internal/layout"
)

// LastBoot is the metadata persisted after a healthy boot. DeploymentID
// is empty on non-ostree systems where no bootc deployment exists.
type LastBoot struct {
	Version      string `json:"version"`
	DeploymentID string `json:"deploymentId,omitempty"`
	BootID       string `json:"bootId,omitempty"`
}

// ReadLastBoot returns the persisted metadata. The bool is false when no
// last-boot record exists (fresh install or post-reset).
func ReadLastBoot(l layout.Layout) (LastBoot, bool, error) {
	b, err := os.ReadFile(l.LastBootFile)
	if errors.Is(err, os.ErrNotExist) {
		return LastBoot{}, false, nil
	}
	if err != nil {
		return LastBoot{}, false, fmt.Errorf("read last-boot: %w", err)
	}
	var lb LastBoot
	if err := json.Unmarshal(b, &lb); err != nil {
		return LastBoot{}, false, fmt.Errorf("parse last-boot: %w", err)
	}
	return lb, true, nil
}

// WriteLastBoot records lb atomically.
func WriteLastBoot(l layout.Layout, lb LastBoot) error {
	data, err := json.Marshal(lb)
	if err != nil {
		return err
	}
	return writeAtomic(l.LastBootFile, data)
}

// WriteLastEvent records msg as the most recent lifecycle event.
func WriteLastEvent(l layout.Layout, msg string) error {
	return writeAtomic(l.LastEventFile, []byte(msg+"\n"))
}

// ReadLastEvent returns the recorded event, or "" if none exists.
func ReadLastEvent(l layout.Layout) (string, error) {
	b, err := os.ReadFile(l.LastEventFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Exists reports whether the node already carries picokube-managed state
// that init would conflict with. Two independent signals:
//
//   - <KubernetesDir>/manifests/kube-apiserver.yaml — kubeadm.Ensure
//     wrote the static pod manifest, so init has run.
//   - <PicoKubeVarDir> — lifecycle has persisted state (last-boot.json,
//     backups, …) from a prior cluster.
//
// Either alone is reason to refuse a fresh init; the second guards
// against the case where /etc/kubernetes was wiped manually but
// lifecycle data still references the old cluster, which would corrupt
// the next boot's upgrade-detection / backup-naming logic.
// `picokube reset` wipes both, so the operator-recovery path is uniform.
func Exists(l layout.Layout) (bool, error) {
	for _, p := range []string{
		l.KubeAPIServerManifest,
		l.PicoKubeVarDir,
	} {
		ok, err := fileExists(p)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func fileExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// writeAtomic writes data to path via a sibling temp file + rename so
// readers never see a half-written file.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
