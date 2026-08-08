package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	content := []byte("hello obsynk")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("HashFile() = %q, want %q", got, want)
	}
}

func TestHashFileStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("repeat me"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("HashFile() not stable: %q != %q", first, second)
	}
}

func TestHashFileDiffersOnContentChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")

	if err := os.WriteFile(path, []byte("version one"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("version two"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Error("expected different hashes for different content")
	}
}
