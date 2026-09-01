package syncengine

import (
	"context"
	"fmt"
	"os"
	stdpath "path"
	"path/filepath"
	"time"

	"obsynk/internal/drive"
	"obsynk/internal/model"
	"obsynk/internal/scanner"
	"obsynk/internal/statestore"
)

func parseDriveTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

// FileEventKind mirrors the plugin's vault event kinds without depending on
// the generated gRPC types, keeping this package buildable/testable on its
// own.
type FileEventKind int

const (
	EventModify FileEventKind = iota
	EventCreate
	EventDelete
	EventRename
)

type FileEvent struct {
	RelativePath string
	Kind         FileEventKind
	// OldRelativePath is set only for EventRename.
	OldRelativePath string
}

// Engine is the daemon-lifetime coordinator: owns the Drive client and
// statestore, and exposes the two entrypoints the gRPC server calls.
type Engine struct {
	VaultRoot   string
	DriveRootID string
	Drive       *drive.Client
	Store       *statestore.Store
	Workers     int
}

// FullSync scans the entire vault and the entire Drive tree and reconciles
// everything. Used for the manual "Sync now" command and for the first
// sync right after connecting Drive. The returned int is the total number
// of operations in the batch, known before the channel starts yielding
// results, so callers can report progress as a fraction/percentage.
func (e *Engine) FullSync(ctx context.Context) (<-chan OpResult, int, error) {
	local, err := e.scanLocal(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("syncengine: scanning local vault: %w", err)
	}

	remote, folderIDs, err := e.scanRemote(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("syncengine: scanning drive tree: %w", err)
	}

	baseline := e.Store.Snapshot()
	if err := guardAgainstEmptyRemote(remote, baseline); err != nil {
		return nil, 0, err
	}

	ops := Plan(Diff(local, remote, baseline))
	executor := NewExecutor(e.Drive, e.Store, e.VaultRoot, e.DriveRootID, e.newFolderCache(folderIDs), e.Workers)
	return executor.Run(ctx, ops), len(ops), nil
}

// newFolderCache builds the folder cache for one sync. seed carries
// directories already read off Drive during this sync (a full tree
// listing); everything else is resolved on demand, preferring the folder
// IDs remembered in the statestore over a search.
func (e *Engine) newFolderCache(seed map[string]string) *folderCache {
	return newFolderCache(e.DriveRootID, seed, folderDeps{
		create:  e.Drive.EnsureChildFolder,
		verify:  e.Drive.VerifyFolder,
		persist: e.Store,
	})
}

// SyncPaths reconciles only the paths implicated by a debounced batch of
// vault events, avoiding a full-vault rescan on every save. Rename events
// are special-cased into a direct, metadata-only Drive rename/move when the
// old path's Drive counterpart is already known; otherwise they fall back
// to the generic path-by-path reconciliation (which will see the old path
// as absent and the new path as untracked, i.e. a fresh create).
func (e *Engine) SyncPaths(ctx context.Context, events []FileEvent) (<-chan OpResult, int, error) {
	// One cache for the whole sync, shared between rename handling and the
	// executor: a rename into a directory and an upload into that same
	// directory must agree on which Drive folder it is.
	folders := e.newFolderCache(nil)

	pathSet := make(map[string]struct{}, len(events))
	for _, ev := range events {
		if ev.Kind == EventRename {
			handled, err := e.handleRename(ctx, folders, ev.OldRelativePath, ev.RelativePath)
			if err != nil {
				return nil, 0, err
			}
			if handled {
				continue
			}
		}
		pathSet[filepath.ToSlash(ev.RelativePath)] = struct{}{}
	}

	relPaths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		relPaths = append(relPaths, p)
	}

	return e.syncRelPaths(ctx, folders, relPaths)
}

// syncRelPaths scans only the given relative paths locally, and resolves
// their remote counterparts by Drive file ID (from the baseline manifest)
// rather than listing the whole Drive tree -- Drive's rate limit, not local
// work, is the real per-sync cost, so this keeps a single-file save cheap.
//
// Known limitation: a path with no baseline record (never synced before)
// is treated as remote-absent without checking Drive, since there's no
// cached file ID to look up directly. If a file of the same name was also
// created independently on Drive without ever syncing, this will create a
// duplicate rather than detect the conflict. Acceptable for a
// single-writer personal vault; a full FullSync (or the periodic
// reconciliation flagged as a future mitigation for remote-only changes)
// would catch and resolve it.
func (e *Engine) syncRelPaths(ctx context.Context, folders *folderCache, relPaths []string) (<-chan OpResult, int, error) {
	local := make(map[string]model.FileMetadata)
	remote := make(map[string]RemoteEntry)
	baseline := make(map[string]model.SyncRecord)

	for _, relPath := range relPaths {
		relPath = filepath.ToSlash(relPath)
		if scanner.IsIgnored(relPath, scanner.DefaultIgnoreGlobs) {
			continue
		}

		absPath := filepath.Join(e.VaultRoot, filepath.FromSlash(relPath))
		if info, statErr := os.Stat(absPath); statErr == nil {
			if !info.IsDir() {
				hash, hashErr := scanner.HashFile(absPath)
				if hashErr != nil {
					return nil, 0, fmt.Errorf("syncengine: hashing %s: %w", relPath, hashErr)
				}
				local[relPath] = model.FileMetadata{
					Path:         absPath,
					RelativePath: relPath,
					Name:         info.Name(),
					Size:         info.Size(),
					ModifiedTime: info.ModTime(),
					Hash:         hash,
				}
			}
		} else if !os.IsNotExist(statErr) {
			return nil, 0, fmt.Errorf("syncengine: stat %s: %w", relPath, statErr)
		}

		rec, ok := e.Store.Get(relPath)
		if !ok {
			continue
		}
		baseline[relPath] = rec

		if rec.DriveFileID == "" {
			continue
		}
		meta, err := e.Drive.GetMetadata(ctx, rec.DriveFileID)
		if err != nil {
			return nil, 0, fmt.Errorf("syncengine: fetching drive metadata for %s: %w", relPath, err)
		}
		if meta.Trashed {
			continue
		}
		modTime, _ := parseDriveTime(meta.ModifiedTime)
		parentID := rec.DriveParentID
		if len(meta.Parents) > 0 {
			parentID = meta.Parents[0]
		}
		remote[relPath] = RemoteEntry{
			RelativePath: relPath,
			FileID:       meta.Id,
			MD5:          meta.Md5Checksum,
			ModTime:      modTime,
			ParentID:     parentID,
		}
		// Deliberately not seeding the folder cache with parentID here. It's
		// where this file currently sits on Drive, which is only the folder
		// for parentOf(relPath) as long as nobody has moved the file --
		// seeding it would send the rest of the batch's uploads wherever a
		// single moved file happens to live. The folder cache resolves the
		// directory itself, from IDs recorded for directories.
	}

	ops := Plan(Diff(local, remote, baseline))
	executor := NewExecutor(e.Drive, e.Store, e.VaultRoot, e.DriveRootID, folders, e.Workers)
	return executor.Run(ctx, ops), len(ops), nil
}

// handleRename performs a direct, metadata-only Drive rename/move for a
// vault rename event, when the old path's Drive counterpart is already
// known. Returns handled=false (no error) when there's no baseline to act
// on, so the caller falls back to generic reconciliation.
func (e *Engine) handleRename(ctx context.Context, folders *folderCache, oldRel, newRel string) (handled bool, err error) {
	oldRel = filepath.ToSlash(oldRel)
	newRel = filepath.ToSlash(newRel)

	rec, ok := e.Store.Get(oldRel)
	if !ok || rec.DriveFileID == "" {
		return false, nil
	}

	newParentID, ferr := folders.resolve(ctx, parentOf(newRel))
	if ferr != nil {
		return false, fmt.Errorf("syncengine: ensuring folder for rename target %s: %w", newRel, ferr)
	}

	newName := stdpath.Base(newRel)
	if err := e.Drive.Rename(ctx, rec.DriveFileID, newName, newParentID, rec.DriveParentID); err != nil {
		return false, fmt.Errorf("syncengine: renaming %s -> %s on drive: %w", oldRel, newRel, err)
	}

	e.Store.Delete(oldRel)
	rec.RelativePath = newRel
	rec.DriveParentID = newParentID
	e.Store.Set(rec)
	return true, nil
}

func (e *Engine) scanLocal(ctx context.Context) (map[string]model.FileMetadata, error) {
	results, err := scanner.Scan(ctx, scanner.Options{
		VaultRoot:   e.VaultRoot,
		IgnoreGlobs: scanner.DefaultIgnoreGlobs,
		Workers:     e.Workers,
	})
	if err != nil {
		return nil, err
	}

	out := make(map[string]model.FileMetadata)
	for r := range results {
		if r.Err != nil {
			return nil, r.Err
		}
		out[r.Meta.RelativePath] = r.Meta
	}
	return out, nil
}

func (e *Engine) scanRemote(ctx context.Context) (map[string]RemoteEntry, map[string]string, error) {
	tree, err := e.Drive.ListVaultTree(ctx, e.DriveRootID)
	if err != nil {
		return nil, nil, err
	}

	files := make(map[string]RemoteEntry)
	folderIDs := map[string]string{"": e.DriveRootID}
	for relPath, entry := range tree {
		if entry.IsDir {
			folderIDs[relPath] = entry.FileID
			continue
		}
		files[relPath] = RemoteEntry{
			RelativePath: relPath,
			FileID:       entry.FileID,
			MD5:          entry.MD5,
			ModTime:      entry.ModTime,
			ParentID:     entry.ParentID,
		}
	}
	return files, folderIDs, nil
}
