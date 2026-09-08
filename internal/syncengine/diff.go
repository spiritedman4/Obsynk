// Package syncengine implements the three-way reconciliation between a
// local vault scan, a Google Drive listing, and the last-synced baseline
// manifest, deciding per path whether to push, pull, delete, or resolve a
// conflict via last-write-wins.
package syncengine

import (
	"time"

	"obsynk/internal/model"
)

type Decision int

const (
	// DecisionNone means both sides already agree; nothing to do.
	DecisionNone Decision = iota
	// DecisionPush uploads local content that changed since the baseline.
	DecisionPush
	// DecisionPull downloads remote content that changed since the baseline.
	DecisionPull
	// DecisionConflict means both sides changed since the baseline;
	// resolved by comparing modification times (see DiffResult.Winner).
	DecisionConflict
	// DecisionPushDelete propagates a local deletion to Drive.
	DecisionPushDelete
	// DecisionPullDelete propagates a remote deletion (trash) locally.
	DecisionPullDelete
	// DecisionCreateLocal downloads a file that exists on Drive but was
	// never synced before and has no local copy.
	DecisionCreateLocal
	// DecisionCreateRemote uploads a file that exists locally but was never
	// synced before and has no Drive copy.
	DecisionCreateRemote
	// DecisionMoveLocal relocates a local file whose Drive counterpart moved
	// to a different path since the last sync, identified by Drive file ID.
	// Without this a move arrives as a delete at the old path plus a create
	// at the new one, which re-downloads content that is already on disk and,
	// worse, leaves the old path with no baseline record -- so a machine that
	// still holds the file there re-uploads it and resurrects the move's
	// source on Drive.
	DecisionMoveLocal
)

func (d Decision) String() string {
	switch d {
	case DecisionNone:
		return "none"
	case DecisionPush:
		return "push"
	case DecisionPull:
		return "pull"
	case DecisionConflict:
		return "conflict"
	case DecisionPushDelete:
		return "push_delete"
	case DecisionPullDelete:
		return "pull_delete"
	case DecisionCreateLocal:
		return "create_local"
	case DecisionCreateRemote:
		return "create_remote"
	case DecisionMoveLocal:
		return "move_local"
	default:
		return "unknown"
	}
}

// RemoteEntry is a simplified view of one Drive file, independent of the
// Google API's own types so this package can be built and tested before
// internal/drive exists.
type RemoteEntry struct {
	RelativePath string
	FileID       string
	MD5          string
	ModTime      time.Time
	// ParentID is the Drive folder ID this file currently lives in, kept so
	// a later rename/move can remove the correct old parent rather than
	// leaving the file multi-parented.
	ParentID string
}

// DiffResult is the reconciliation outcome for one relative path.
type DiffResult struct {
	RelativePath string
	Decision     Decision

	Local    *model.FileMetadata
	Remote   *RemoteEntry
	Baseline *model.SyncRecord

	// Winner is "local" or "remote", set only when Decision ==
	// DecisionConflict.
	Winner string

	// OldRelativePath is where the file currently sits locally, set only
	// when Decision == DecisionMoveLocal. Local and Baseline describe that
	// old path; RelativePath and Remote describe where it belongs now.
	OldRelativePath string
}

// Diff compares the current local scan, the current Drive listing, and the
// persisted baseline manifest, producing one DiffResult per path that is
// present in at least one of the three. It is a pure function: no I/O, so
// it's fully unit-testable against fixture maps.
func Diff(local map[string]model.FileMetadata, remote map[string]RemoteEntry, baseline map[string]model.SyncRecord) []DiffResult {
	paths := make(map[string]struct{}, len(local)+len(remote)+len(baseline))
	for p := range local {
		paths[p] = struct{}{}
	}
	for p := range remote {
		paths[p] = struct{}{}
	}
	for p := range baseline {
		paths[p] = struct{}{}
	}

	// Moves are resolved before anything else: both endpoints of a move are
	// in paths, and each would otherwise be judged on its own as a deletion
	// and an unrelated creation.
	movedTo := detectMoves(local, remote, baseline)
	movedFrom := make(map[string]struct{}, len(movedTo))
	for _, old := range movedTo {
		movedFrom[old] = struct{}{}
	}

	results := make([]DiffResult, 0, len(paths))
	for p := range paths {
		if _, isSource := movedFrom[p]; isSource {
			// Consumed by the move emitted at its destination.
			continue
		}
		if oldPath, moved := movedTo[p]; moved {
			oldLocal := local[oldPath]
			oldBaseline := baseline[oldPath]
			newRemote := remote[p]
			results = append(results, DiffResult{
				RelativePath:    p,
				OldRelativePath: oldPath,
				Decision:        DecisionMoveLocal,
				Local:           &oldLocal,
				Remote:          &newRemote,
				Baseline:        &oldBaseline,
			})
			continue
		}

		l, hasLocal := local[p]
		r, hasRemote := remote[p]
		b, hasBaseline := baseline[p]

		dr := DiffResult{RelativePath: p}
		if hasLocal {
			lCopy := l
			dr.Local = &lCopy
		}
		if hasRemote {
			rCopy := r
			dr.Remote = &rCopy
		}
		if hasBaseline {
			bCopy := b
			dr.Baseline = &bCopy
		}

		if !hasBaseline {
			dr.Decision, dr.Winner = decideNoBaseline(hasLocal, hasRemote, l, r)
		} else {
			dr.Decision, dr.Winner = decideWithBaseline(hasLocal, hasRemote, l, r, b)
		}

		results = append(results, dr)
	}
	return results
}

func decideNoBaseline(hasLocal, hasRemote bool, l model.FileMetadata, r RemoteEntry) (Decision, string) {
	switch {
	case hasLocal && !hasRemote:
		return DecisionCreateRemote, ""
	case !hasLocal && hasRemote:
		return DecisionCreateLocal, ""
	case hasLocal && hasRemote:
		return DecisionConflict, resolveWinner(l.ModifiedTime, r.ModTime)
	default:
		return DecisionNone, ""
	}
}

func decideWithBaseline(hasLocal, hasRemote bool, l model.FileMetadata, r RemoteEntry, b model.SyncRecord) (Decision, string) {
	localChanged := !hasLocal || l.Hash != b.LocalHash
	remoteChanged := !hasRemote || r.MD5 != b.DriveMD5

	switch {
	case !localChanged && !remoteChanged:
		return DecisionNone, ""
	case localChanged && !remoteChanged:
		if hasLocal {
			return DecisionPush, ""
		}
		return DecisionPushDelete, ""
	case !localChanged && remoteChanged:
		if hasRemote {
			return DecisionPull, ""
		}
		return DecisionPullDelete, ""
	default: // both changed
		if !hasLocal && !hasRemote {
			// Both sides deleted independently: nothing to transfer, just
			// a stale baseline entry to garbage-collect.
			return DecisionNone, ""
		}
		var lt, rt time.Time
		if hasLocal {
			lt = l.ModifiedTime
		}
		if hasRemote {
			rt = r.ModTime
		}
		return DecisionConflict, resolveWinner(lt, rt)
	}
}

// resolveWinner implements last-write-wins: the strictly newer mtime wins.
// An exact tie (including the no-baseline case where one side has a zero
// time) defaults to local, deterministically, since some choice must be
// made and this is a real data-loss edge case worth documenting explicitly
// rather than leaving to map/slice iteration order.
func resolveWinner(localTime, remoteTime time.Time) string {
	if remoteTime.After(localTime) {
		return "remote"
	}
	return "local"
}

// detectMoves finds files that moved on Drive since the last sync, returning
// newPath -> oldPath. Matching is by Drive file ID: it is the only identity
// that survives a move, since local hashes are sha256 and Drive reports md5,
// so content cannot be compared across the boundary.
//
// Only unambiguous moves qualify. The destination must be untracked and
// unoccupied locally, the source must still be on disk, and the content must
// be unchanged on both sides -- anything else is a move tangled up with an
// edit, and is left to the ordinary per-path rules, which are conservative
// and will not lose data.
func detectMoves(local map[string]model.FileMetadata, remote map[string]RemoteEntry, baseline map[string]model.SyncRecord) map[string]string {
	byFileID := make(map[string]string, len(baseline))
	for path, rec := range baseline {
		if rec.DriveFileID != "" && !rec.Deleted {
			byFileID[rec.DriveFileID] = path
		}
	}

	moves := make(map[string]string)
	for newPath, r := range remote {
		if r.FileID == "" {
			continue
		}
		oldPath, known := byFileID[r.FileID]
		if !known || oldPath == newPath {
			continue
		}
		if _, tracked := baseline[newPath]; tracked {
			continue
		}
		if _, occupied := local[newPath]; occupied {
			continue
		}
		oldLocal, present := local[oldPath]
		if !present {
			continue
		}
		oldBaseline := baseline[oldPath]
		if oldLocal.Hash != oldBaseline.LocalHash || r.MD5 != oldBaseline.DriveMD5 {
			continue
		}
		moves[newPath] = oldPath
	}
	return moves
}
