package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanRespectsIgnoreGlobs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "note.md"), "a")
	writeFile(t, filepath.Join(root, "sub", "other.md"), "b")
	writeFile(t, filepath.Join(root, ".obsidian", "manifest.json"), "{}")
	writeFile(t, filepath.Join(root, ".obsidian", "plugins", "obsynk", "token.json"), "secret")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main")
	writeFile(t, filepath.Join(root, ".trash", "deleted.md"), "gone")

	results, err := Scan(context.Background(), Options{
		VaultRoot:   root,
		IgnoreGlobs: DefaultIgnoreGlobs,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected scan error: %v", r.Err)
		}
		got = append(got, r.Meta.RelativePath)
	}
	sort.Strings(got)

	want := []string{"note.md", "sub/other.md"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestScanOneResultPerFile(t *testing.T) {
	root := t.TempDir()
	const n = 20
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(root, "notes", string(rune('a'+i))+".md"), "content")
	}

	results, err := Scan(context.Background(), Options{VaultRoot: root, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}

	count := 0
	for r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected scan error: %v", r.Err)
		}
		if r.Meta.Hash == "" {
			t.Errorf("expected non-empty hash for %s", r.Meta.RelativePath)
		}
		count++
	}
	if count != n {
		t.Errorf("got %d results, want %d", count, n)
	}
}

func TestMatchIgnore(t *testing.T) {
	cases := []struct {
		relPath string
		want    bool
	}{
		{".obsidian/manifest.json", true},
		{".obsidian/plugins/obsynk/token.json", true},
		{".git/HEAD", true},
		{".trash/note.md", true},
		{"note.md", false},
		{"sub/note.md", false},
		{"notobsidian/file.md", false},
	}
	for _, c := range cases {
		got := matchIgnore(c.relPath, DefaultIgnoreGlobs)
		if got != c.want {
			t.Errorf("matchIgnore(%q) = %v, want %v", c.relPath, got, c.want)
		}
	}
}
