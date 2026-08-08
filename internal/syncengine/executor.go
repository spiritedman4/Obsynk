package syncengine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"context"

	"obsynk/internal/drive"
	"obsynk/internal/model"
	"obsynk/internal/scanner"
	"obsynk/internal/statestore"
)

// OpResult is the outcome of executing one Operation, streamed back to the
// gRPC caller as it completes.
type OpResult struct {
	Op  Operation
	Err error
}

// Executor carries out a Plan's Operations against Drive and the local
// filesystem, updating the statestore as each completes, with bounded
// concurrency (Drive's per-user rate limit, not local I/O, is the real
// ceiling here).
type Executor struct {
	drive     *drive.Client
	store     *statestore.Store
	vaultRoot string
	rootID    string
	workers   int

	folderMu  sync.Mutex
	folderIDs map[string]string // relative dir path ("" = root) -> Drive folder ID
}

// NewExecutor builds an Executor. folderIDs may be nil or pre-seeded (e.g.
// from a prior full Drive tree listing) to avoid redundant EnsureSubfolders
// calls for directories already known.
func NewExecutor(driveClient *drive.Client, store *statestore.Store, vaultRoot, rootID string, folderIDs map[string]string, workers int) *Executor {
	if folderIDs == nil {
		folderIDs = make(map[string]string)
	}
	if workers <= 0 {
		workers = 4
	}
	return &Executor{
		drive:     driveClient,
		store:     store,
		vaultRoot: vaultRoot,
		rootID:    rootID,
		workers:   workers,
		folderIDs: folderIDs,
	}
}

// Run executes ops with bounded worker-pool concurrency, streaming a result
// per operation as it completes. Partial failure of one op doesn't abort
// the batch.
func (e *Executor) Run(ctx context.Context, ops []Operation) <-chan OpResult {
	results := make(chan OpResult, len(ops))
	if len(ops) == 0 {
		close(results)
		return results
	}

	opCh := make(chan Operation)
	var wg sync.WaitGroup
	wg.Add(e.workers)
	for i := 0; i < e.workers; i++ {
		go func() {
			defer wg.Done()
			for op := range opCh {
				err := e.execute(ctx, op)
				results <- OpResult{Op: op, Err: err}
			}
		}()
	}

	go func() {
		defer close(opCh)
		for _, op := range ops {
			select {
			case opCh <- op:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}

func (e *Executor) execute(ctx context.Context, op Operation) error {
	switch op.Kind {
	case DecisionCreateRemote, DecisionPush:
		return e.push(ctx, op)
	case DecisionCreateLocal, DecisionPull:
		return e.pull(ctx, op)
	case DecisionPushDelete:
		return e.pushDelete(ctx, op)
	case DecisionPullDelete:
		return e.pullDelete(ctx, op)
	case DecisionConflict:
		if op.Winner == "remote" {
			return e.pull(ctx, op)
		}
		return e.push(ctx, op)
	default:
		return fmt.Errorf("syncengine: unknown operation kind %v", op.Kind)
	}
}

func (e *Executor) push(ctx context.Context, op Operation) error {
	if op.Local == nil {
		return fmt.Errorf("syncengine: push %s: missing local metadata", op.RelativePath)
	}

	f, err := os.Open(op.Local.Path)
	if err != nil {
		return fmt.Errorf("syncengine: opening %s: %w", op.Local.Path, err)
	}
	defer f.Close()

	parentID, err := e.ensureParentFolder(ctx, op.RelativePath)
	if err != nil {
		return err
	}

	var fileID, md5 string
	if op.Baseline != nil && op.Baseline.DriveFileID != "" {
		fileID = op.Baseline.DriveFileID
		md5, err = e.drive.UpdateContent(ctx, fileID, f, op.Local.ModifiedTime)
	} else {
		fileID, md5, err = e.drive.Upload(ctx, parentID, op.Local.Name, f, op.Local.ModifiedTime)
	}
	if err != nil {
		return fmt.Errorf("syncengine: pushing %s: %w", op.RelativePath, err)
	}

	e.store.Set(model.SyncRecord{
		RelativePath:   op.RelativePath,
		LocalHash:      op.Local.Hash,
		LocalModTime:   op.Local.ModifiedTime,
		DriveFileID:    fileID,
		DriveMD5:       md5,
		DriveModTime:   op.Local.ModifiedTime,
		DriveParentID:  parentID,
		LastSyncedHash: op.Local.Hash,
	})
	return nil
}

func (e *Executor) pull(ctx context.Context, op Operation) error {
	if op.Remote == nil {
		return fmt.Errorf("syncengine: pull %s: missing remote metadata", op.RelativePath)
	}

	rc, err := e.drive.Download(ctx, op.Remote.FileID)
	if err != nil {
		return fmt.Errorf("syncengine: downloading %s: %w", op.RelativePath, err)
	}
	defer rc.Close()

	absPath := filepath.Join(e.vaultRoot, filepath.FromSlash(op.RelativePath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("syncengine: creating local dir for %s: %w", op.RelativePath, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(absPath), ".obsynk-download-*")
	if err != nil {
		return fmt.Errorf("syncengine: creating temp file for %s: %w", op.RelativePath, err)
	}
	tmpName := tmp.Name()

	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("syncengine: writing %s: %w", op.RelativePath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("syncengine: closing temp file for %s: %w", op.RelativePath, err)
	}
	if err := os.Rename(tmpName, absPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("syncengine: renaming into place %s: %w", op.RelativePath, err)
	}
	if err := os.Chtimes(absPath, op.Remote.ModTime, op.Remote.ModTime); err != nil {
		return fmt.Errorf("syncengine: setting mtime for %s: %w", op.RelativePath, err)
	}

	hash, err := scanner.HashFile(absPath)
	if err != nil {
		return fmt.Errorf("syncengine: hashing downloaded %s: %w", op.RelativePath, err)
	}

	e.store.Set(model.SyncRecord{
		RelativePath:   op.RelativePath,
		LocalHash:      hash,
		LocalModTime:   op.Remote.ModTime,
		DriveFileID:    op.Remote.FileID,
		DriveMD5:       op.Remote.MD5,
		DriveModTime:   op.Remote.ModTime,
		DriveParentID:  op.Remote.ParentID,
		LastSyncedHash: hash,
	})
	return nil
}

func (e *Executor) pushDelete(ctx context.Context, op Operation) error {
	if op.Baseline == nil || op.Baseline.DriveFileID == "" {
		e.store.Delete(op.RelativePath)
		return nil
	}
	if err := e.drive.Trash(ctx, op.Baseline.DriveFileID); err != nil {
		return fmt.Errorf("syncengine: push-deleting %s: %w", op.RelativePath, err)
	}
	e.store.Delete(op.RelativePath)
	return nil
}

func (e *Executor) pullDelete(_ context.Context, op Operation) error {
	absPath := filepath.Join(e.vaultRoot, filepath.FromSlash(op.RelativePath))
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("syncengine: pull-deleting %s: %w", op.RelativePath, err)
	}
	e.store.Delete(op.RelativePath)
	return nil
}

// ensureParentFolder resolves (creating if needed) the Drive folder for
// relPath's parent directory, caching the result for the lifetime of this
// Executor so files landing in the same new directory within one batch
// don't repeat the lookup/creation.
func (e *Executor) ensureParentFolder(ctx context.Context, relPath string) (string, error) {
	dir := parentOf(relPath)

	e.folderMu.Lock()
	if id, ok := e.folderIDs[dir]; ok {
		e.folderMu.Unlock()
		return id, nil
	}
	e.folderMu.Unlock()

	id, err := e.drive.EnsureSubfolders(ctx, e.rootID, dir)
	if err != nil {
		return "", fmt.Errorf("syncengine: ensuring folder for %s: %w", relPath, err)
	}

	e.folderMu.Lock()
	e.folderIDs[dir] = id
	e.folderMu.Unlock()
	return id, nil
}
