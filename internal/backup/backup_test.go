package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/layouttest"
	"github.com/MatchaScript/picokube/internal/state"
)

// requireCp skips the test when the host `cp` binary does not accept the
// flags runCp relies on (`--reflink=auto`). Every Linux distro we care
// about ships GNU coreutils, so this only guards test runs on macOS/BSD.
func requireCp(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cp"); err != nil {
		t.Skipf("cp not in PATH: %v", err)
	}
	out, err := exec.Command("cp", "--help").CombinedOutput()
	if err != nil || !containsAll(string(out), "--reflink") {
		t.Skip("host cp lacks --reflink; skipping (picokube targets GNU coreutils)")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !fileContains(s, sub) {
			return false
		}
	}
	return true
}

func fileContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// newStaging mirrors what preflight.AllocateWorkspace allocates for
// backup.Create's pre-rename scratch area. Tests call this before each
// Create to give it a freshly-empty staging dir on the same filesystem
// as l.BackupsDir, exactly as the production caller does.
func newStaging(t *testing.T, l layout.Layout) string {
	t.Helper()
	if err := os.MkdirAll(l.BackupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(l.BackupsDir, ".staging")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	return staging
}

// finalDir is the on-disk path Create renames its staging into for meta.
func finalDir(l layout.Layout, meta state.LastBoot) string {
	return filepath.Join(l.BackupsDir, Name(meta))
}

// seedLiveState populates the tempdir-rooted live trees with
// representative content so Create has something to snapshot.
func seedLiveState(t *testing.T, l layout.Layout) {
	t.Helper()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(l.EtcdDataDir, 0o700))
	must(os.WriteFile(filepath.Join(l.EtcdDataDir, "member"), []byte("etcd-data"), 0o600))
	must(os.MkdirAll(l.KubernetesDir, 0o755))
	must(os.WriteFile(filepath.Join(l.KubernetesDir, "admin.conf"), []byte("kc"), 0o644))
	must(os.MkdirAll(l.ManifestsDir, 0o755))
	must(os.WriteFile(filepath.Join(l.ManifestsDir, "kube-apiserver.yaml"), []byte("manifest"), 0o644))
	must(os.MkdirAll(l.KubeletDir, 0o755))
	for _, f := range []string{"config.yaml", "instance-config.yaml", "kubeadm-flags.env"} {
		must(os.WriteFile(filepath.Join(l.KubeletDir, f), []byte("x="+f), 0o644))
	}
}

func TestBootID_MatchesHostBootID(t *testing.T) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Skipf("/proc/sys/kernel/random/boot_id unavailable: %v", err)
	}
	id, err := BootID()
	if err != nil {
		t.Fatalf("BootID: %v", err)
	}
	want := string(raw)
	// BootID trims whitespace; the raw file ends in '\n'.
	if id == "" || id+"\n" != want {
		t.Errorf("BootID = %q, want %q (trimmed)", id, want)
	}
}

func TestName_IsDeploymentUnderscoreBoot(t *testing.T) {
	n := Name(state.LastBoot{DeploymentID: "dep", BootID: "boot"})
	if n != "dep_boot" {
		t.Errorf("Name = %q, want dep_boot", n)
	}
}

func TestCreate_RejectsEmptyMeta(t *testing.T) {
	l := layouttest.New(t)
	cases := []state.LastBoot{
		{DeploymentID: "", BootID: "b"},
		{DeploymentID: "d", BootID: ""},
		{},
	}
	for i, m := range cases {
		// Use a throwaway staging path; meta validation must reject before
		// Create touches the filesystem.
		if err := Create(filepath.Join(l.BackupsDir, ".staging"), finalDir(l, m), m, l); err == nil {
			t.Errorf("case %d: Create(%+v) = nil; want error", i, m)
		}
	}
}

func TestCreate_CapturesAllThreeTrees(t *testing.T) {
	requireCp(t)
	l := layouttest.New(t)
	seedLiveState(t, l)

	meta := state.LastBoot{Version: "v1.35.0", DeploymentID: "dep1", BootID: "boot1"}
	if err := Create(newStaging(t, l), finalDir(l, meta), meta, l); err != nil {
		t.Fatalf("Create: %v", err)
	}

	backupRoot := finalDir(l, meta)
	for _, rel := range []string{
		"meta.json",
		"etcd/member",
		"kubernetes/admin.conf",
		"kubernetes/manifests/kube-apiserver.yaml",
		"kubelet/config.yaml",
		"kubelet/instance-config.yaml",
		"kubelet/kubeadm-flags.env",
	} {
		p := filepath.Join(backupRoot, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("backup missing %s: %v", rel, err)
		}
	}
}

func TestCreate_IsIdempotent(t *testing.T) {
	requireCp(t)
	l := layouttest.New(t)
	seedLiveState(t, l)

	meta := state.LastBoot{Version: "v1.35.0", DeploymentID: "dep1", BootID: "boot1"}
	if err := Create(newStaging(t, l), finalDir(l, meta), meta, l); err != nil {
		t.Fatal(err)
	}
	firstStat, err := os.Stat(filepath.Join(finalDir(l, meta), "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately change live state; idempotent Create must not overwrite
	// the existing backup with the new contents.
	if err := os.WriteFile(filepath.Join(l.KubernetesDir, "admin.conf"), []byte("mutated"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // ensure mtime would differ on a real rewrite
	if err := Create(newStaging(t, l), finalDir(l, meta), meta, l); err != nil {
		t.Fatal(err)
	}

	secondStat, err := os.Stat(filepath.Join(finalDir(l, meta), "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !firstStat.ModTime().Equal(secondStat.ModTime()) {
		t.Errorf("Create overwrote existing backup (mtime changed)")
	}

	preserved, err := os.ReadFile(filepath.Join(finalDir(l, meta), "kubernetes/admin.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != "kc" {
		t.Errorf("admin.conf in backup = %q; should have been the original %q", string(preserved), "kc")
	}
}

// Missing source trees (etcd absent on first boot) must not break
// Create — copyDirIfExists tolerates absent sources. After success the
// staging dir was renamed away, so it no longer exists alongside the
// final dir.
func TestCreate_TolerantOfMissingSources(t *testing.T) {
	requireCp(t)
	l := layouttest.New(t)
	// Deliberately do NOT call seedLiveState so that sources are missing.
	meta := state.LastBoot{DeploymentID: "d", BootID: "b"}
	staging := newStaging(t, l)
	if err := Create(staging, finalDir(l, meta), meta, l); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(finalDir(l, meta)); err != nil {
		t.Fatalf("final dir missing after success: %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging dir survived rename: err=%v", err)
	}
}

func TestRestore_SwapsInBackupContents(t *testing.T) {
	requireCp(t)
	l := layouttest.New(t)
	seedLiveState(t, l)

	meta := state.LastBoot{Version: "v1", DeploymentID: "d", BootID: "b"}
	if err := Create(newStaging(t, l), finalDir(l, meta), meta, l); err != nil {
		t.Fatal(err)
	}

	// Mutate live state — restore must put it back.
	if err := os.WriteFile(filepath.Join(l.KubernetesDir, "admin.conf"), []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.EtcdDataDir, "member"), []byte("MUTATED"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.KubeletDir, "config.yaml"), []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Restore(l, Name(meta)); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for path, want := range map[string]string{
		filepath.Join(l.KubernetesDir, "admin.conf"):     "kc",
		filepath.Join(l.EtcdDataDir, "member"):           "etcd-data",
		filepath.Join(l.KubeletDir, "config.yaml"):       "x=config.yaml",
		filepath.Join(l.KubeletDir, "kubeadm-flags.env"): "x=kubeadm-flags.env",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q; want %q (Restore did not put back)", path, string(got), want)
		}
	}
}

func TestRestore_MissingEtcdInBackupWipesLiveEtcd(t *testing.T) {
	requireCp(t)
	l := layouttest.New(t)

	// Seed a backup that deliberately lacks the etcd subtree (mimics a
	// first-boot snapshot where etcd had not yet initialised).
	meta := state.LastBoot{Version: "v1", DeploymentID: "d", BootID: "b"}
	backupDir := filepath.Join(l.BackupsDir, Name(meta))
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "meta.json"), []byte(`{"version":"v1","deploymentId":"d","bootId":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Live etcd has content from a newer boot that must be wiped.
	if err := os.MkdirAll(l.EtcdDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.EtcdDataDir, "junk"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Restore(l, Name(meta)); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if _, err := os.Stat(l.EtcdDataDir); !os.IsNotExist(err) {
		t.Errorf("live etcd dir survived restore: err=%v", err)
	}
}

func TestRestore_UnknownBackupFails(t *testing.T) {
	l := layouttest.New(t)
	if err := Restore(l, "nonexistent_backup"); err == nil {
		t.Fatal("Restore of unknown backup = nil; want error")
	}
}

func TestReadMeta_RoundTrip(t *testing.T) {
	requireCp(t)
	l := layouttest.New(t)
	seedLiveState(t, l)

	in := state.LastBoot{Version: "v9.9.9", DeploymentID: "deploy", BootID: "kern-boot"}
	if err := Create(newStaging(t, l), finalDir(l, in), in, l); err != nil {
		t.Fatal(err)
	}
	out, err := ReadMeta(l, Name(in))
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round trip lost data: got %+v, want %+v", out, in)
	}
}

func TestList_SkipsTmpAndNonMatchingEntries(t *testing.T) {
	l := layouttest.New(t)
	if err := os.MkdirAll(l.BackupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{
		"dep1_boot1",
		"dep1_boot2",
		"dep2_boot1.tmp",
		"dep3_boot1.restoring",
		"noUnderscoreHere",
	} {
		if err := os.Mkdir(filepath.Join(l.BackupsDir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Also drop a file (not a dir); must be skipped.
	if err := os.WriteFile(filepath.Join(l.BackupsDir, "restore"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	names, err := List(l)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	want := []string{"dep1_boot1", "dep1_boot2"}
	if len(names) != len(want) {
		t.Fatalf("List() = %v; want %v", names, want)
	}
	for i := range names {
		if names[i] != want[i] {
			t.Errorf("List()[%d] = %q; want %q", i, names[i], want[i])
		}
	}
}

func TestList_MissingDirIsEmpty(t *testing.T) {
	l := layouttest.New(t)
	names, err := List(l)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("List on missing dir = %v; want empty", names)
	}
}

func TestLatestForDeployment_PicksNewestMtime(t *testing.T) {
	l := layouttest.New(t)
	if err := os.MkdirAll(l.BackupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"dep1_old", "dep1_new", "dep2_only"} {
		if err := os.Mkdir(filepath.Join(l.BackupsDir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Force mtimes so ordering is unambiguous.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(l.BackupsDir, "dep1_old"), now, now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(l.BackupsDir, "dep1_new"), now, now); err != nil {
		t.Fatal(err)
	}

	got, err := LatestForDeployment(l, "dep1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "dep1_new" {
		t.Errorf("LatestForDeployment(dep1) = %q; want dep1_new", got)
	}

	got, err = LatestForDeployment(l, "dep-missing")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("LatestForDeployment(unknown) = %q; want empty", got)
	}
}

func TestPrune_DropsUnknownDeploymentsAndKeepsNewestPerKnown(t *testing.T) {
	l := layouttest.New(t)
	if err := os.MkdirAll(l.BackupsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	mk := func(name string, age time.Duration) {
		p := filepath.Join(l.BackupsDir, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	mk("keep_old", 1*time.Hour)
	mk("keep_new", 0)
	mk("gone_boot1", 0)
	mk("solo_boot1", 0)

	if err := Prune(l, []string{"keep", "solo"}); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	for _, want := range []string{"keep_new", "solo_boot1"} {
		if _, err := os.Stat(filepath.Join(l.BackupsDir, want)); err != nil {
			t.Errorf("Prune removed %s: %v", want, err)
		}
	}
	for _, gone := range []string{"keep_old", "gone_boot1"} {
		if _, err := os.Stat(filepath.Join(l.BackupsDir, gone)); !os.IsNotExist(err) {
			t.Errorf("Prune did not remove %s: %v", gone, err)
		}
	}
}

func TestRestoreMarker_CreatesConsumesClears(t *testing.T) {
	l := layouttest.New(t)
	if err := os.MkdirAll(l.BackupsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Initially absent.
	yes, err := RestoreRequested(l)
	if err != nil {
		t.Fatal(err)
	}
	if yes {
		t.Error("RestoreRequested true on fresh state")
	}

	// Place marker (mirrors what greenboot red.d does).
	if err := os.WriteFile(l.RestoreMarker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	yes, err = RestoreRequested(l)
	if err != nil {
		t.Fatal(err)
	}
	if !yes {
		t.Error("RestoreRequested false after marker placed")
	}

	// Clear.
	if err := ClearRestoreMarker(l); err != nil {
		t.Fatal(err)
	}
	yes, err = RestoreRequested(l)
	if err != nil {
		t.Fatal(err)
	}
	if yes {
		t.Error("RestoreRequested true after ClearRestoreMarker")
	}
	// ClearRestoreMarker must be idempotent.
	if err := ClearRestoreMarker(l); err != nil {
		t.Errorf("ClearRestoreMarker on already-absent marker = %v; want nil", err)
	}
}

func TestExists_ReportsPresence(t *testing.T) {
	l := layouttest.New(t)
	ok, err := Exists(l, "dep_boot")
	if err != nil || ok {
		t.Errorf("Exists on empty = ok=%v err=%v", ok, err)
	}
	if err := os.MkdirAll(filepath.Join(l.BackupsDir, "dep_boot"), 0o700); err != nil {
		t.Fatal(err)
	}
	ok, err = Exists(l, "dep_boot")
	if err != nil || !ok {
		t.Errorf("Exists on present = ok=%v err=%v", ok, err)
	}
}
