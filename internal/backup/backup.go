// Package backup implements picokube's boot-time snapshot/restore via
// directory copies. Only ostree / bootc systems take backups — the
// snapshot window relies on etcd being stopped (picokube.service runs
// Before=kubelet.service) and restore is only meaningful when there is
// an underlying bootc deployment to tie backups to. The caller (package
// lifecycle) is responsible for gating the entry points on
// ostree.IsOSTree().
//
// Layout under /var/lib/picokube/backups:
//
//	restore                 — marker file placed by greenboot red.d
//	                          just before bootc rollback; consumed on
//	                          the next boot and removed after restore.
//	<deployID>_<bootID>/    — one backup per successful boot, named
//	                          after the deployment+boot whose data is
//	                          contained.
//	  meta.json             — Version/DeploymentID/BootID of that boot
//	  etcd/                 — mirror of /var/lib/etcd
//	  kubernetes/           — mirror of /etc/kubernetes
//	  kubelet/
//	    config.yaml
//	    instance-config.yaml
//	    kubeadm-flags.env
//
// Create writes into a caller-owned staging directory and finalises
// with os.Rename to atomically reveal the snapshot under its final
// name; preflight.Workspace owns the staging dir and its cleanup, so
// Create itself is a pure mechanism with no rollback responsibility.
// A failed Create therefore returns the error and stops; the deferred
// preflight cleanup wipes whatever partial copy is left in staging.
// Restore stages per-target via `<dst>.restoring` siblings and
// finalises with os.Rename, mirroring microshift's AtomicDirCopy
// pattern (reference/microshift/pkg/admin/data/atomic_dir_copy.go);
// it cleans its own staging on each failure path because the staging
// lives at the production target's parent (e.g. /var/lib/etcd.restoring),
// not in the preflight workspace.
package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	atomicpkg "github.com/MatchaScript/picokube/internal/atomic"
	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/state"
)

// backupEntry pairs a backup directory name with its on-disk modtime,
// used by sortByModTimeDesc.
type backupEntry struct {
	name  string
	mtime time.Time
}

// sortByModTimeDesc stats each name under l.BackupsDir and returns
// entries sorted newest-first. List/Stat races where a concurrent prune
// removed the entry are tolerated (fs.ErrNotExist is skipped); any
// other stat error propagates so we never silently dereference a nil
// FileInfo (CR3).
func sortByModTimeDesc(l layout.Layout, names []string) ([]backupEntry, error) {
	out := make([]backupEntry, 0, len(names))
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(l.BackupsDir, n))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat backup %s: %w", n, err)
		}
		out = append(out, backupEntry{n, fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
	return out, nil
}

// kubeletStateFiles enumerates the /var/lib/kubelet files worth
// preserving. Other entries (pods, plugins, pki) are regenerable and
// would bloat the backup considerably.
var kubeletStateFiles = []string{
	"config.yaml",
	"instance-config.yaml",
	"kubeadm-flags.env",
}

const metaFileName = "meta.json"

// BootID returns the current kernel boot id.
func BootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read boot_id: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Name composes the on-disk directory name for a backup.
func Name(meta state.LastBoot) string {
	return meta.DeploymentID + "_" + meta.BootID
}

// Exists reports whether a backup with this name is already on disk.
func Exists(l layout.Layout, name string) (bool, error) {
	_, err := os.Stat(filepath.Join(l.BackupsDir, name))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Create snapshots the current on-disk state (etcd + kubernetes +
// selected kubelet files) into stagingDir, then atomically renames it
// to finalDir. If finalDir already exists the call is a no-op
// (idempotent on the same boot's meta).
//
// stagingDir MUST be a sibling of finalDir on the same filesystem and
// MUST be pre-allocated empty by the caller (backup.Workspace.BackupTmp
// fulfils both contracts). On error Create returns and stops; cleanup of
// any partial copy in stagingDir is the caller's responsibility, handled
// uniformly by the deferred cleanup of the backup-owned workspace.
func Create(stagingDir, finalDir string, meta state.LastBoot, l layout.Layout) error {
	if meta.DeploymentID == "" || meta.BootID == "" {
		return errors.New("backup requires DeploymentID and BootID")
	}
	if _, err := os.Stat(finalDir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", finalDir, err)
	}

	if err := copyDirIfExists(l.EtcdDataDir, filepath.Join(stagingDir, "etcd")); err != nil {
		return fmt.Errorf("copy etcd: %w", err)
	}
	if err := copyDirIfExists(l.KubernetesDir, filepath.Join(stagingDir, "kubernetes")); err != nil {
		return fmt.Errorf("copy kubernetes: %w", err)
	}
	kubeletDst := filepath.Join(stagingDir, "kubelet")
	if err := os.MkdirAll(kubeletDst, 0o700); err != nil {
		return fmt.Errorf("mkdir kubelet: %w", err)
	}
	for _, f := range kubeletStateFiles {
		src := filepath.Join(l.KubeletDir, f)
		dst := filepath.Join(kubeletDst, f)
		if err := copyFileIfExists(src, dst); err != nil {
			return fmt.Errorf("copy kubelet/%s: %w", f, err)
		}
	}
	if err := writeMeta(stagingDir, meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	if err := os.Rename(stagingDir, finalDir); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", stagingDir, finalDir, err)
	}
	return nil
}

// ReadMeta returns the metadata recorded inside a backup directory.
func ReadMeta(l layout.Layout, name string) (state.LastBoot, error) {
	b, err := os.ReadFile(filepath.Join(l.BackupsDir, name, metaFileName))
	if err != nil {
		return state.LastBoot{}, fmt.Errorf("read %s meta: %w", name, err)
	}
	var meta state.LastBoot
	if err := json.Unmarshal(b, &meta); err != nil {
		return state.LastBoot{}, fmt.Errorf("parse %s meta: %w", name, err)
	}
	return meta, nil
}

// Restore swaps the live state trees with the contents of
// backups/<name>/. Each target (etcd, kubernetes, selected kubelet
// files) is staged under an intermediate path and atomically renamed.
func Restore(l layout.Layout, name string) error {
	src := filepath.Join(l.BackupsDir, name)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("stat backup %s: %w", name, err)
	}

	if err := restoreDir(filepath.Join(src, "etcd"), l.EtcdDataDir); err != nil {
		return fmt.Errorf("restore etcd: %w", err)
	}
	if err := restoreDir(filepath.Join(src, "kubernetes"), l.KubernetesDir); err != nil {
		return fmt.Errorf("restore kubernetes: %w", err)
	}
	if err := os.MkdirAll(l.KubeletDir, 0o755); err != nil {
		return err
	}
	for _, f := range kubeletStateFiles {
		srcFile := filepath.Join(src, "kubelet", f)
		dstFile := filepath.Join(l.KubeletDir, f)
		if _, err := os.Stat(srcFile); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(dstFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove live %s: %w", f, err)
			}
			continue
		} else if err != nil {
			return err
		}
		if err := replaceFile(srcFile, dstFile); err != nil {
			return fmt.Errorf("restore kubelet/%s: %w", f, err)
		}
	}
	return nil
}

// List returns all backup directory names, excluding work-in-progress
// `.tmp` / `.restoring` artefacts.
func List(l layout.Layout) ([]string, error) {
	entries, err := os.ReadDir(l.BackupsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".tmp") || strings.HasSuffix(n, ".restoring") {
			continue
		}
		if _, _, ok := strings.Cut(n, "_"); !ok {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// LatestForDeployment returns the name of the most recent backup
// belonging to deploymentID. Returns "" if none exists.
func LatestForDeployment(l layout.Layout, deploymentID string) (string, error) {
	names, err := List(l)
	if err != nil {
		return "", err
	}
	prefix := deploymentID + "_"
	matches := make([]string, 0, len(names))
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			matches = append(matches, n)
		}
	}
	sorted, err := sortByModTimeDesc(l, matches)
	if err != nil {
		return "", err
	}
	if len(sorted) == 0 {
		return "", nil
	}
	return sorted[0].name, nil
}

// LatestSize returns the on-disk size in bytes of the most recent
// backup, summed across every regular file under its directory tree.
// Returns 0 (without error) when no backups exist — the typical state
// after `picokube init` and before the first reboot snapshot. Used by
// the preflight check to size the next snapshot's headroom.
func LatestSize(l layout.Layout) (uint64, error) {
	names, err := List(l)
	if err != nil {
		return 0, err
	}
	sorted, err := sortByModTimeDesc(l, names)
	if err != nil {
		return 0, err
	}
	if len(sorted) == 0 {
		return 0, nil
	}
	return dirSize(filepath.Join(l.BackupsDir, sorted[0].name))
}

func dirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += uint64(info.Size())
		return nil
	})
	return total, err
}

// Prune drops backups whose deployment id is no longer known to the
// system (bootc has GCed it) and, for still-known deployments, keeps
// only the most recent backup per deployment.
func Prune(l layout.Layout, knownDeployments []string) error {
	known := make(map[string]bool, len(knownDeployments))
	for _, d := range knownDeployments {
		known[d] = true
	}
	names, err := List(l)
	if err != nil {
		return err
	}
	byDeploy := map[string][]string{}
	for _, n := range names {
		deploy, _, _ := strings.Cut(n, "_")
		if !known[deploy] {
			if err := os.RemoveAll(filepath.Join(l.BackupsDir, n)); err != nil {
				return fmt.Errorf("prune %s: %w", n, err)
			}
			continue
		}
		byDeploy[deploy] = append(byDeploy[deploy], n)
	}
	for _, group := range byDeploy {
		if len(group) <= 1 {
			continue
		}
		sorted, err := sortByModTimeDesc(l, group)
		if err != nil {
			return fmt.Errorf("sort backups for prune: %w", err)
		}
		// Keep the newest, prune the rest.
		for _, entry := range sorted[1:] {
			if err := os.RemoveAll(filepath.Join(l.BackupsDir, entry.name)); err != nil {
				return fmt.Errorf("prune %s: %w", entry.name, err)
			}
		}
	}
	return nil
}

// RestoreRequested reports whether the external restore marker is present.
func RestoreRequested(l layout.Layout) (bool, error) {
	_, err := os.Stat(l.RestoreMarker)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// ClearRestoreMarker removes the marker after restore has been handled.
func ClearRestoreMarker(l layout.Layout) error {
	if err := os.Remove(l.RestoreMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ---- internals ----

func writeMeta(dir string, meta state.LastBoot) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metaFileName), b, 0o600)
}

// restoreDir replaces dst with the contents of src via an intermediate
// sibling of dst. When src is absent the backup did not cover this
// tree (first-boot etcd case), so dst is wiped to keep restore total.
//
// CR1: uses atomic.SwapDir (RENAME_EXCHANGE) so the live data is never
// in a half-state on disk. The old pattern `RemoveAll(dst) ->
// Rename(staged, dst)` had a window where a power loss between the two
// calls left dst gone — etcd-fatal. After SwapDir, the displaced data
// is at `staged` and we RemoveAll it; if power loss happens between
// the swap and the cleanup, the next boot finds a `.restoring` dir
// next to the freshly-restored live tree which a startup helper can
// safely delete (or which the next restore overwrites).
func restoreDir(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove live %s: %w", dst, err)
		}
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	staged := dst + ".restoring"
	if err := os.RemoveAll(staged); err != nil {
		return err
	}
	if err := copyTree(src, staged); err != nil {
		_ = os.RemoveAll(staged)
		return err
	}
	if err := atomicpkg.SwapDir(staged, dst); err != nil {
		_ = os.RemoveAll(staged)
		return err
	}
	// staged now holds the displaced old live data; drop it. If this
	// fails we still have a valid live dst, so the failure is
	// noisy-but-not-fatal — log via caller, do not bubble.
	if err := os.RemoveAll(staged); err != nil {
		return fmt.Errorf("remove displaced live %s: %w", staged, err)
	}
	return nil
}

// replaceFile swaps dst for src using a sibling of dst as the staging
// path, so the rename is atomic on the destination filesystem.
func replaceFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	staged := dst + ".restoring"
	if err := os.Remove(staged); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := copyFile(src, staged); err != nil {
		_ = os.Remove(staged)
		return err
	}
	return os.Rename(staged, dst)
}

// copyDirIfExists copies src to dst when src exists; otherwise it is a
// no-op (dst is not created). Used for the etcd-absent first-boot case.
func copyDirIfExists(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return copyTree(src, dst)
}

func copyFileIfExists(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return copyFile(src, dst)
}

// copyTree copies an entire directory src into a new dst using
// `cp --reflink=auto` so that Copy-on-Write filesystems (xfs, btrfs)
// clone extents instead of duplicating bytes. dst must not exist.
func copyTree(src, dst string) error {
	return runCp(src, dst,
		"--recursive",
		"--no-target-directory",
		"--preserve=mode,ownership,timestamps,links",
		"--reflink=auto",
	)
}

func copyFile(src, dst string) error {
	return runCp(src, dst,
		"--preserve=mode,ownership,timestamps,links",
		"--reflink=auto",
	)
}

func runCp(src, dst string, flags ...string) error {
	args := append([]string{}, flags...)
	args = append(args, src, dst)
	cmd := exec.Command("cp", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cp %v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
