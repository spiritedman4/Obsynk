package statestore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"obsynk/internal/model"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}

	rec := model.SyncRecord{
		RelativePath:   "note.md",
		LocalHash:      "abc123",
		LocalModTime:   time.Now().Truncate(time.Second),
		DriveFileID:    "drive-id",
		DriveMD5:       "def456",
		DriveModTime:   time.Now().Truncate(time.Second),
		LastSyncedHash: "abc123",
	}
	s.Set(rec)

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get("note.md")
	if !ok {
		t.Fatal("expected record to round-trip")
	}

	if got.RelativePath != rec.RelativePath ||
		got.LocalHash != rec.LocalHash ||
		got.DriveFileID != rec.DriveFileID ||
		got.DriveMD5 != rec.DriveMD5 ||
		got.LastSyncedHash != rec.LastSyncedHash ||
		got.Deleted != rec.Deleted {
		t.Errorf("got %+v, want %+v", got, rec)
	}
	if !got.LocalModTime.Equal(rec.LocalModTime) {
		t.Errorf("LocalModTime = %v, want %v", got.LocalModTime, rec.LocalModTime)
	}
	if !got.DriveModTime.Equal(rec.DriveModTime) {
		t.Errorf("DriveModTime = %v, want %v", got.DriveModTime, rec.DriveModTime)
	}
}

func TestStoreOpenMissingFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot()) != 0 {
		t.Errorf("expected empty manifest, got %d records", len(s.Snapshot()))
	}
}

func TestStoreDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}

	s.Set(model.SyncRecord{RelativePath: "a.md", LocalHash: "1"})
	if _, ok := s.Get("a.md"); !ok {
		t.Fatal("expected a.md to be present after Set")
	}

	s.Delete("a.md")
	if _, ok := s.Get("a.md"); ok {
		t.Fatal("expected a.md to be gone after Delete")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel := "notes/" + string(rune('a'+i%26)) + ".md"
			s.Set(model.SyncRecord{RelativePath: rel, LocalHash: "h"})
			s.Get(rel)
			s.Snapshot()
		}(i)
	}
	wg.Wait()

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreFolderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}

	s.SetFolder("AI & ML", "folder-1")
	s.SetFolder("AI & ML/papers", "folder-2")
	// Nothing to say about the root, and an empty ID is not a mapping.
	s.SetFolder("", "folder-root")
	s.SetFolder("Empty", "")

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}

	for dir, want := range map[string]string{"AI & ML": "folder-1", "AI & ML/papers": "folder-2"} {
		got, ok := reopened.FolderID(dir)
		if !ok || got != want {
			t.Errorf("FolderID(%q) = %q, %v; want %q, true", dir, got, ok, want)
		}
	}
	for _, dir := range []string{"", "Empty"} {
		if _, ok := reopened.FolderID(dir); ok {
			t.Errorf("FolderID(%q) unexpectedly present", dir)
		}
	}

	reopened.DeleteFolder("AI & ML")
	if _, ok := reopened.FolderID("AI & ML"); ok {
		t.Error("expected the folder mapping to be gone after DeleteFolder")
	}
}

func TestStoreMigratesV1Manifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	// A v1 manifest, written before Folders existed.
	v1 := `{
  "version": 1,
  "vaultPath": "/vault",
  "driveRootId": "root-id",
  "records": {"note.md": {"RelativePath": "note.md", "LocalHash": "abc"}}
}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatalf("opening a v1 manifest: %v", err)
	}
	if _, ok := s.Get("note.md"); !ok {
		t.Error("migration dropped an existing record")
	}
	if _, ok := s.FolderID("anything"); ok {
		t.Error("migrated manifest should start with no remembered folders")
	}

	// Writing into the new map must work on a migrated manifest, not panic
	// on a nil one.
	s.SetFolder("Docker", "folder-1")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.FolderID("Docker"); !ok || got != "folder-1" {
		t.Errorf("FolderID(Docker) = %q, %v after migration", got, ok)
	}
	if reopened.manifest.Version != model.ManifestVersion {
		t.Errorf("manifest version = %d, want %d", reopened.manifest.Version, model.ManifestVersion)
	}
}

func TestStoreRefusesNewerManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	future := `{"version": 99, "vaultPath": "/vault", "records": {}}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path, "/vault"); err == nil {
		t.Fatal("expected Open to refuse a manifest from a newer version rather than rewriting it")
	}
}

func TestAtomicWriteLeavesValidFileAcrossRewrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}

	s.Set(model.SyncRecord{RelativePath: "a.md", LocalHash: "1"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	firstRead, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	s.Set(model.SyncRecord{RelativePath: "b.md", LocalHash: "2"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	secondRead, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(firstRead) == string(secondRead) {
		t.Error("expected manifest content to change after second save")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "manifest.json" {
			t.Errorf("expected no leftover temp files, found %q", e.Name())
		}
	}
}

func TestStoreAdoptRootRecordsFirstRootWithoutDiscarding(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "manifest.json"), "/vault")
	if err != nil {
		t.Fatal(err)
	}
	s.Set(model.SyncRecord{RelativePath: "a.md", LocalHash: "1"})
	s.SetFolder("notes", "folder-1")

	// A manifest predating root tracking has no root recorded. Adopting it
	// must keep the baseline -- wiping here would destroy sync state for
	// every existing vault on upgrade.
	if got := s.AdoptRoot("root-a"); got != RootRecorded {
		t.Errorf("AdoptRoot on an untracked manifest = %v, want RootRecorded", got)
	}
	if _, ok := s.Get("a.md"); !ok {
		t.Error("recording a root for the first time discarded the baseline")
	}
	if _, ok := s.FolderID("notes"); !ok {
		t.Error("recording a root for the first time discarded the folder map")
	}

	if got := s.AdoptRoot("root-a"); got != RootUnchanged {
		t.Errorf("re-adopting the same root = %v, want RootUnchanged", got)
	}
	if _, ok := s.Get("a.md"); !ok {
		t.Error("re-adopting the same root discarded the baseline")
	}
}

func TestStoreAdoptRootDiscardsBaselineWhenRootChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	s, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}
	s.AdoptRoot("root-a")
	s.Set(model.SyncRecord{RelativePath: "a.md", LocalHash: "1", DriveFileID: "file-in-old-root"})
	s.SetFolder("notes", "folder-in-old-root")

	// Pointing the daemon at a different Drive folder makes every record a
	// claim about files that have nothing to do with it.
	if got := s.AdoptRoot("root-b"); got != RootReset {
		t.Fatalf("AdoptRoot with a different root = %v, want RootReset", got)
	}
	if _, ok := s.Get("a.md"); ok {
		t.Error("kept a baseline record pointing into the old Drive root")
	}
	if _, ok := s.FolderID("notes"); ok {
		t.Error("kept a folder ID pointing into the old Drive root")
	}

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, "/vault")
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.AdoptRoot("root-b"); got != RootUnchanged {
		t.Errorf("reopened manifest did not persist the new root: %v", got)
	}
}

func TestStoreAdoptRootIgnoresEmptyRoot(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "manifest.json"), "/vault")
	if err != nil {
		t.Fatal(err)
	}
	s.AdoptRoot("root-a")
	s.Set(model.SyncRecord{RelativePath: "a.md", LocalHash: "1"})

	// Drive not connected yet: no root to compare against, so nothing to do.
	if got := s.AdoptRoot(""); got != RootUnchanged {
		t.Errorf("AdoptRoot(\"\") = %v, want RootUnchanged", got)
	}
	if _, ok := s.Get("a.md"); !ok {
		t.Error("an empty root discarded the baseline")
	}
}
