package syncengine

import (
	"os"
	"path/filepath"
	"testing"

	"obsynk/internal/model"
	"obsynk/internal/statestore"
)

func decisionsByPath(results []DiffResult) map[string]Decision {
	out := make(map[string]Decision, len(results))
	for _, r := range results {
		out[r.RelativePath] = r.Decision
	}
	return out
}

// The reported bug: a file moved on one machine arrived on the other as a
// second copy, with the original left in place. Treated per path, the move
// is a deletion here and an unrelated creation there.
func TestDiffDetectsRemoteMoveAsOneOperation(t *testing.T) {
	t.Parallel()

	local := map[string]model.FileMetadata{
		"Notes/a.md": {RelativePath: "Notes/a.md", Hash: "H1"},
	}
	remote := map[string]RemoteEntry{
		"Archive/a.md": {RelativePath: "Archive/a.md", FileID: "X", MD5: "M1", ParentID: "archive-folder"},
	}
	baseline := map[string]model.SyncRecord{
		"Notes/a.md": {RelativePath: "Notes/a.md", LocalHash: "H1", DriveFileID: "X", DriveMD5: "M1"},
	}

	results := Diff(local, remote, baseline)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 move: %v", len(results), decisionsByPath(results))
	}
	r := results[0]
	if r.Decision != DecisionMoveLocal {
		t.Errorf("decision = %s, want move_local", r.Decision)
	}
	if r.RelativePath != "Archive/a.md" || r.OldRelativePath != "Notes/a.md" {
		t.Errorf("move %q -> %q, want Notes/a.md -> Archive/a.md", r.OldRelativePath, r.RelativePath)
	}
	// The old path must not also be reported: emitting a create_remote for it
	// is what re-uploaded the stale copy and undid the move on Drive.
	if d, ok := decisionsByPath(results)["Notes/a.md"]; ok {
		t.Errorf("old path also produced %s", d)
	}
}

// Anything less than an unambiguous move falls back to the ordinary rules
// rather than renaming a file out from under a change.
func TestDiffLeavesAmbiguousMovesAlone(t *testing.T) {
	t.Parallel()

	base := func() (map[string]model.FileMetadata, map[string]RemoteEntry, map[string]model.SyncRecord) {
		return map[string]model.FileMetadata{
				"Notes/a.md": {RelativePath: "Notes/a.md", Hash: "H1"},
			},
			map[string]RemoteEntry{
				"Archive/a.md": {RelativePath: "Archive/a.md", FileID: "X", MD5: "M1"},
			},
			map[string]model.SyncRecord{
				"Notes/a.md": {RelativePath: "Notes/a.md", LocalHash: "H1", DriveFileID: "X", DriveMD5: "M1"},
			}
	}

	t.Run("local edited since last sync", func(t *testing.T) {
		t.Parallel()
		local, remote, baseline := base()
		local["Notes/a.md"] = model.FileMetadata{RelativePath: "Notes/a.md", Hash: "EDITED"}
		for _, r := range Diff(local, remote, baseline) {
			if r.Decision == DecisionMoveLocal {
				t.Error("renamed a file that had local edits")
			}
		}
	})

	t.Run("content also changed remotely", func(t *testing.T) {
		t.Parallel()
		local, remote, baseline := base()
		remote["Archive/a.md"] = RemoteEntry{RelativePath: "Archive/a.md", FileID: "X", MD5: "M2"}
		for _, r := range Diff(local, remote, baseline) {
			if r.Decision == DecisionMoveLocal {
				t.Error("treated a move-plus-edit as a plain move")
			}
		}
	})

	t.Run("destination already occupied locally", func(t *testing.T) {
		t.Parallel()
		local, remote, baseline := base()
		local["Archive/a.md"] = model.FileMetadata{RelativePath: "Archive/a.md", Hash: "OTHER"}
		for _, r := range Diff(local, remote, baseline) {
			if r.Decision == DecisionMoveLocal {
				t.Error("would have overwritten an unrelated file at the destination")
			}
		}
	})

	t.Run("source no longer on disk", func(t *testing.T) {
		t.Parallel()
		_, remote, baseline := base()
		results := Diff(map[string]model.FileMetadata{}, remote, baseline)
		for _, r := range results {
			if r.Decision == DecisionMoveLocal {
				t.Error("tried to move a file that isn't there")
			}
		}
		if got := decisionsByPath(results)["Archive/a.md"]; got != DecisionCreateLocal {
			t.Errorf("Archive/a.md = %s, want create_local", got)
		}
	})
}

func TestExecutorMoveLocalRenamesAndReparentsRecord(t *testing.T) {
	t.Parallel()

	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "Notes", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(vault, "Notes", "sub", "a.md")
	if err := os.WriteFile(oldPath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Executor{vaultRoot: vault, store: newTestStore(t)}
	op := Operation{
		Kind:            DecisionMoveLocal,
		RelativePath:    "Archive/a.md",
		OldRelativePath: "Notes/sub/a.md",
		Local:           &model.FileMetadata{RelativePath: "Notes/sub/a.md", Hash: "H1"},
		Remote:          &RemoteEntry{RelativePath: "Archive/a.md", FileID: "X", MD5: "M1", ParentID: "archive-folder"},
		Baseline:        &model.SyncRecord{RelativePath: "Notes/sub/a.md", LocalHash: "H1", DriveFileID: "X", DriveMD5: "M1"},
	}
	if err := e.moveLocal(op); err != nil {
		t.Fatalf("moveLocal: %v", err)
	}

	if _, err := os.Stat(filepath.Join(vault, "Archive", "a.md")); err != nil {
		t.Errorf("file not at its new path: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("original still present after the move")
	}
	// Emptied directories are pruned; nothing else ever removes them.
	if _, err := os.Stat(filepath.Join(vault, "Notes")); !os.IsNotExist(err) {
		t.Error("emptied Notes/ left behind")
	}

	if _, ok := e.store.Get("Notes/sub/a.md"); ok {
		t.Error("baseline record kept at the old path")
	}
	rec, ok := e.store.Get("Archive/a.md")
	if !ok {
		t.Fatal("no baseline record at the new path")
	}
	if rec.DriveParentID != "archive-folder" || rec.DriveFileID != "X" || rec.LocalHash != "H1" {
		t.Errorf("record not carried over correctly: %+v", rec)
	}
}

func TestPruneEmptyDirsStopsAtVaultRootAndNonEmpty(t *testing.T) {
	t.Parallel()

	vault := t.TempDir()
	keep := filepath.Join(vault, "Keep")
	if err := os.MkdirAll(filepath.Join(keep, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "other.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pruneEmptyDirs(vault, filepath.Join(keep, "empty"))

	if _, err := os.Stat(filepath.Join(keep, "empty")); !os.IsNotExist(err) {
		t.Error("empty dir not pruned")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("pruned a directory that still had a file in it")
	}
	if _, err := os.Stat(vault); err != nil {
		t.Error("pruned past the vault root")
	}
}

func newTestStore(t *testing.T) *statestore.Store {
	t.Helper()
	s, err := statestore.Open(filepath.Join(t.TempDir(), "manifest.json"), "/vault")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
