package syncengine

import (
	"testing"
	"time"

	"obsynk/internal/model"
)

const relPath = "note.md"

var (
	t1 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 = time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC) // strictly after t1
)

func localMeta(hash string, modTime time.Time) model.FileMetadata {
	return model.FileMetadata{RelativePath: relPath, Hash: hash, ModifiedTime: modTime}
}

func remoteEntry(md5 string, modTime time.Time) RemoteEntry {
	return RemoteEntry{RelativePath: relPath, MD5: md5, ModTime: modTime}
}

func baselineRecord(localHash, driveMD5 string) model.SyncRecord {
	return model.SyncRecord{RelativePath: relPath, LocalHash: localHash, DriveMD5: driveMD5, LastSyncedHash: localHash}
}

func diffOne(local map[string]model.FileMetadata, remote map[string]RemoteEntry, baseline map[string]model.SyncRecord) DiffResult {
	results := Diff(local, remote, baseline)
	for _, r := range results {
		if r.RelativePath == relPath {
			return r
		}
	}
	panic("no result for " + relPath)
}

func TestDiff_NoBaseline_LocalOnly(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h1", t1)}
	got := diffOne(local, nil, nil)
	if got.Decision != DecisionCreateRemote {
		t.Errorf("Decision = %v, want %v", got.Decision, DecisionCreateRemote)
	}
}

func TestDiff_NoBaseline_RemoteOnly(t *testing.T) {
	remote := map[string]RemoteEntry{relPath: remoteEntry("m1", t1)}
	got := diffOne(nil, remote, nil)
	if got.Decision != DecisionCreateLocal {
		t.Errorf("Decision = %v, want %v", got.Decision, DecisionCreateLocal)
	}
}

func TestDiff_NoBaseline_BothPresent_LocalNewerWins(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h1", t2)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m1", t1)}
	got := diffOne(local, remote, nil)
	if got.Decision != DecisionConflict {
		t.Fatalf("Decision = %v, want %v", got.Decision, DecisionConflict)
	}
	if got.Winner != "local" {
		t.Errorf("Winner = %q, want %q", got.Winner, "local")
	}
}

func TestDiff_NoBaseline_BothPresent_RemoteNewerWins(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h1", t1)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m1", t2)}
	got := diffOne(local, remote, nil)
	if got.Decision != DecisionConflict {
		t.Fatalf("Decision = %v, want %v", got.Decision, DecisionConflict)
	}
	if got.Winner != "remote" {
		t.Errorf("Winner = %q, want %q", got.Winner, "remote")
	}
}

func TestDiff_NoBaseline_BothPresent_ExactTieDefaultsLocal(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h1", t1)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m1", t1)}
	got := diffOne(local, remote, nil)
	if got.Decision != DecisionConflict {
		t.Fatalf("Decision = %v, want %v", got.Decision, DecisionConflict)
	}
	if got.Winner != "local" {
		t.Errorf("Winner = %q, want %q (exact tie should default to local)", got.Winner, "local")
	}
}

func TestDiff_Baseline_Unchanged(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h1", t1)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m1", t1)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(local, remote, baseline)
	if got.Decision != DecisionNone {
		t.Errorf("Decision = %v, want %v", got.Decision, DecisionNone)
	}
}

func TestDiff_Baseline_LocalChangedOnly(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h2", t2)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m1", t1)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(local, remote, baseline)
	if got.Decision != DecisionPush {
		t.Errorf("Decision = %v, want %v", got.Decision, DecisionPush)
	}
}

func TestDiff_Baseline_RemoteChangedOnly(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h1", t1)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m2", t2)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(local, remote, baseline)
	if got.Decision != DecisionPull {
		t.Errorf("Decision = %v, want %v", got.Decision, DecisionPull)
	}
}

func TestDiff_Baseline_LocalDeletedOnly(t *testing.T) {
	remote := map[string]RemoteEntry{relPath: remoteEntry("m1", t1)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(nil, remote, baseline)
	if got.Decision != DecisionPushDelete {
		t.Errorf("Decision = %v, want %v", got.Decision, DecisionPushDelete)
	}
}

func TestDiff_Baseline_RemoteDeletedOnly(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h1", t1)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(local, nil, baseline)
	if got.Decision != DecisionPullDelete {
		t.Errorf("Decision = %v, want %v", got.Decision, DecisionPullDelete)
	}
}

func TestDiff_Baseline_BothChanged_LocalNewerWins(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h2", t2)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m2", t1)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(local, remote, baseline)
	if got.Decision != DecisionConflict {
		t.Fatalf("Decision = %v, want %v", got.Decision, DecisionConflict)
	}
	if got.Winner != "local" {
		t.Errorf("Winner = %q, want %q", got.Winner, "local")
	}
}

func TestDiff_Baseline_BothChanged_RemoteNewerWins(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h2", t1)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m2", t2)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(local, remote, baseline)
	if got.Decision != DecisionConflict {
		t.Fatalf("Decision = %v, want %v", got.Decision, DecisionConflict)
	}
	if got.Winner != "remote" {
		t.Errorf("Winner = %q, want %q", got.Winner, "remote")
	}
}

func TestDiff_Baseline_BothChanged_ExactTieDefaultsLocal(t *testing.T) {
	local := map[string]model.FileMetadata{relPath: localMeta("h2", t1)}
	remote := map[string]RemoteEntry{relPath: remoteEntry("m2", t1)}
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(local, remote, baseline)
	if got.Decision != DecisionConflict {
		t.Fatalf("Decision = %v, want %v", got.Decision, DecisionConflict)
	}
	if got.Winner != "local" {
		t.Errorf("Winner = %q, want %q", got.Winner, "local")
	}
}

func TestDiff_Baseline_BothDeleted_GarbageCollected(t *testing.T) {
	baseline := map[string]model.SyncRecord{relPath: baselineRecord("h1", "m1")}
	got := diffOne(nil, nil, baseline)
	if got.Decision != DecisionNone {
		t.Errorf("Decision = %v, want %v (stale baseline should be a no-op)", got.Decision, DecisionNone)
	}
}

func TestDiff_UnionOfMultiplePaths(t *testing.T) {
	local := map[string]model.FileMetadata{
		"a.md": {RelativePath: "a.md", Hash: "h1", ModifiedTime: t1},
		"b.md": {RelativePath: "b.md", Hash: "h2", ModifiedTime: t1},
	}
	remote := map[string]RemoteEntry{
		"c.md": {RelativePath: "c.md", MD5: "m3", ModTime: t1},
	}

	results := Diff(local, remote, nil)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3 (a.md, b.md, c.md)", len(results))
	}

	byPath := make(map[string]DiffResult, len(results))
	for _, r := range results {
		byPath[r.RelativePath] = r
	}
	if byPath["a.md"].Decision != DecisionCreateRemote {
		t.Errorf("a.md Decision = %v, want %v", byPath["a.md"].Decision, DecisionCreateRemote)
	}
	if byPath["b.md"].Decision != DecisionCreateRemote {
		t.Errorf("b.md Decision = %v, want %v", byPath["b.md"].Decision, DecisionCreateRemote)
	}
	if byPath["c.md"].Decision != DecisionCreateLocal {
		t.Errorf("c.md Decision = %v, want %v", byPath["c.md"].Decision, DecisionCreateLocal)
	}
}
