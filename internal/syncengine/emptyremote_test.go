package syncengine

import (
	"errors"
	"testing"

	"obsynk/internal/model"
)

func trackedBaseline(paths ...string) map[string]model.SyncRecord {
	b := make(map[string]model.SyncRecord, len(paths))
	for i, p := range paths {
		b[p] = model.SyncRecord{RelativePath: p, DriveFileID: "file-" + string(rune('a'+i))}
	}
	return b
}

// The scenario this exists for: the Drive root was repointed at an empty
// folder, so the listing is empty while the baseline is full. Diff would
// call that a wholesale remote deletion and remove every local file.
func TestGuardRefusesEmptyRemoteWithTrackedBaseline(t *testing.T) {
	t.Parallel()

	err := guardAgainstEmptyRemote(map[string]RemoteEntry{}, trackedBaseline("a.md", "b.md", "notes/c.md"))
	if err == nil {
		t.Fatal("expected an empty Drive listing to be refused, not treated as 3 deletions")
	}

	var empty *EmptyRemoteListingError
	if !errors.As(err, &empty) {
		t.Fatalf("got %T, want *EmptyRemoteListingError", err)
	}
	if empty.TrackedFiles != 3 {
		t.Errorf("TrackedFiles = %d, want 3", empty.TrackedFiles)
	}
}

func TestGuardAllowsLegitimateSyncs(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		remote   map[string]RemoteEntry
		baseline map[string]model.SyncRecord
	}{
		// First sync ever: nothing on Drive, nothing recorded. Everything
		// here is an upload, not a deletion.
		"empty remote, empty baseline": {map[string]RemoteEntry{}, map[string]model.SyncRecord{}},
		// A real deletion of one file among several still has a non-empty
		// listing, so the guard must stay out of the way.
		"non-empty remote": {
			map[string]RemoteEntry{"a.md": {RelativePath: "a.md", FileID: "x"}},
			trackedBaseline("a.md", "b.md"),
		},
		// Tombstones assert the file is already gone from Drive, so their
		// absence from the listing is expected, not evidence of a problem.
		"empty remote, only tombstones": {
			map[string]RemoteEntry{},
			map[string]model.SyncRecord{"a.md": {RelativePath: "a.md", DriveFileID: "x", Deleted: true}},
		},
		// A record that never reached Drive claims nothing about the listing.
		"empty remote, baseline without drive IDs": {
			map[string]RemoteEntry{},
			map[string]model.SyncRecord{"a.md": {RelativePath: "a.md"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := guardAgainstEmptyRemote(tc.remote, tc.baseline); err != nil {
				t.Errorf("guard blocked a legitimate sync: %v", err)
			}
		})
	}
}
