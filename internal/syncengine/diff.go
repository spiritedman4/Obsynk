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

	results := make([]DiffResult, 0, len(paths))
	for p := range paths {
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
