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
