package syncengine

import (
	"fmt"

	"obsynk/internal/model"
)

// EmptyRemoteListingError reports that a full sync listed the Drive root
// and got nothing back, for a vault whose baseline says files should be
// there.
type EmptyRemoteListingError struct {
	// TrackedFiles is how many live (non-tombstone) files the baseline
	// claims exist on Drive.
	TrackedFiles int
}

func (e *EmptyRemoteListingError) Error() string {
	return fmt.Sprintf(
		"syncengine: Drive returned no files, but the last sync recorded %d of them; refusing to sync because this would delete those files locally. "+
			"If the Drive folder really was emptied, reset the sync state to re-upload; if the Drive root folder was changed, reconnect so the baseline is rebuilt.",
		e.TrackedFiles,
	)
}

// guardAgainstEmptyRemote refuses a full sync whose Drive listing came back
// completely empty while the baseline says files should be there.
//
// Diff reads "absent from the Drive listing" as "deleted on Drive", which
// for an otherwise-unchanged file means DecisionPullDelete -- deleting the
// local copy. That inference is only as trustworthy as the listing, and a
// listing can be wrong for reasons that have nothing to do with the user
// deleting anything: the root folder was repointed at an empty folder, the
// folder was trashed, access was revoked, or a listing call returned an
// empty page. In every one of those the diff concludes the entire vault was
// deleted, and acts on it.
//
// The rule is deliberately blunt -- empty versus not-empty, rather than a
// "more than N%% deleted" threshold. Emptiness is unambiguous and cannot
// false-positive on any real deletion pattern short of genuinely deleting
// everything, which is rare, recoverable by resetting the sync state, and
// worth one confirmation. A percentage threshold would start guessing at
// intent and would eventually block a legitimate bulk cleanup.
//
// Tombstones don't count: a record marked Deleted asserts the file is
// already gone from Drive, so its absence from the listing is expected.
func guardAgainstEmptyRemote(remote map[string]RemoteEntry, baseline map[string]model.SyncRecord) error {
	if len(remote) > 0 {
		return nil
	}

	tracked := 0
	for _, rec := range baseline {
		if !rec.Deleted && rec.DriveFileID != "" {
			tracked++
		}
	}
	if tracked == 0 {
		return nil
	}
	return &EmptyRemoteListingError{TrackedFiles: tracked}
}
