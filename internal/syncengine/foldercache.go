package syncengine

import (
	"context"
	stdpath "path"
	"sync"
)

// createFolderFunc finds-or-creates a single folder named name directly
// under parentID, returning its Drive folder ID.
type createFolderFunc func(ctx context.Context, parentID, name string) (string, error)

// verifyFolderFunc reports whether folderID still names a live folder
// directly under parentID. A stale ID is (false, nil); only a failure to
// check at all is an error.
type verifyFolderFunc func(ctx context.Context, folderID, parentID string) (bool, error)

// folderPersister remembers directory-to-folder-ID mappings across syncs.
// Satisfied by *statestore.Store.
type folderPersister interface {
	FolderID(dir string) (string, bool)
	SetFolder(dir, id string)
	DeleteFolder(dir string)
}

// folderDeps are folderCache's collaborators.
type folderDeps struct {
	// create finds-or-creates one folder by name under a parent. Required.
	create createFolderFunc
	// verify checks a remembered folder ID is still usable. Required
	// whenever persist is set.
	verify verifyFolderFunc
	// persist remembers resolved folder IDs across syncs. Optional; when
	// nil the cache resolves by search alone, as it did before.
	persist folderPersister
}

// folderCache maps relative directory paths ("" = the sync root) to Drive
// folder IDs, resolving each path exactly once even when many workers push
// files into the same directory at the same time.
//
// The serialization is the point, not the caching. Find-or-create on Drive
// is a list-then-create round trip, so without it N workers racing on the
// same missing directory all see an empty list and all create it -- which
// is how a first sync ended up with three or four copies of each folder on
// Drive and the vault's files scattered across them.
//
// Entries are per path component, so siblings ("A/B" and "A/C") still
// resolve concurrently while sharing the single resolution of "A".
//
// The in-memory entries only live as long as one sync batch. Across syncs
// the cache leans on deps.persist, which is what keeps a folder created by
// one batch from being created again by the next: Drive's search index is
// eventually consistent, so a freshly created folder can still be invisible
// to the find-or-create search seconds later.
type folderCache struct {
	rootID string
	deps   folderDeps

	mu      sync.Mutex
	entries map[string]*folderEntry
}

// folderEntry is one directory's resolution, in flight or complete. id and
// err are written only by the goroutine that created the entry, and read by
// others only after ready closes.
type folderEntry struct {
	ready chan struct{}
	id    string
	err   error
}

// newFolderCache builds a cache rooted at rootID. seed pre-populates
// already-known directories, keyed the same way: forward-slash paths
// relative to the root.
//
// Seeds are trusted without verification -- they come from a listing made
// during this same sync -- and are written through to deps.persist, so a
// full sync's tree listing doubles as a refresh of the remembered folder
// IDs.
func newFolderCache(rootID string, seed map[string]string, deps folderDeps) *folderCache {
	fc := &folderCache{
		rootID:  rootID,
		deps:    deps,
		entries: make(map[string]*folderEntry, len(seed)),
	}
	for dir, id := range seed {
		fc.remember(dir, id)
	}
	return fc
}

// remember records a directory's Drive folder ID as already resolved,
// skipping both verification and the search. Only for mappings read
// straight off Drive during this same sync (a tree listing), which are
// fresher than anything stored -- hence the write-through to persist.
func (fc *folderCache) remember(dir, id string) {
	if dir == "" || id == "" {
		return
	}

	fc.mu.Lock()
	if _, exists := fc.entries[dir]; exists {
		fc.mu.Unlock()
		return
	}
	ready := make(chan struct{})
	close(ready)
	fc.entries[dir] = &folderEntry{ready: ready, id: id}
	fc.mu.Unlock()

	if fc.deps.persist != nil {
		fc.deps.persist.SetFolder(dir, id)
	}
}

// resolve returns the Drive folder ID for dir, creating it and any missing
// ancestors. Concurrent callers for the same dir share one resolution.
func (fc *folderCache) resolve(ctx context.Context, dir string) (string, error) {
	if dir == "" || dir == "." {
		return fc.rootID, nil
	}

	fc.mu.Lock()
	if ent, ok := fc.entries[dir]; ok {
		fc.mu.Unlock()
		select {
		case <-ent.ready:
			return ent.id, ent.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ent := &folderEntry{ready: make(chan struct{})}
	fc.entries[dir] = ent
	fc.mu.Unlock()

	// Resolve the parent through the cache as well, so a shared ancestor is
	// created once however many descendants race for it. No lock is held
	// across this call, so the recursion can't deadlock.
	parentID, err := fc.resolve(ctx, parentOf(dir))
	if err != nil {
		ent.err = err
	} else {
		ent.id, ent.err = fc.lookup(ctx, dir, parentID)
	}
	err = ent.err
	close(ent.ready)

	if err != nil {
		// Drop failures rather than caching them: a transient Drive error
		// while creating one folder shouldn't fail every remaining file
		// bound for it.
		fc.mu.Lock()
		if fc.entries[dir] == ent {
			delete(fc.entries, dir)
		}
		fc.mu.Unlock()
		return "", err
	}
	return ent.id, nil
}

// lookup resolves a single directory not already in the in-memory cache:
// reuse the remembered ID if it's still live, otherwise find-or-create by
// name and remember the result.
func (fc *folderCache) lookup(ctx context.Context, dir, parentID string) (string, error) {
	if fc.deps.persist != nil {
		if id, ok := fc.deps.persist.FolderID(dir); ok {
			live, err := fc.deps.verify(ctx, id, parentID)
			if err != nil {
				// Deliberately fail the operation rather than falling back to
				// find-or-create. A failed check says nothing about whether
				// the folder is there, and guessing "it isn't" is how you
				// create a duplicate -- the exact outcome this is here to
				// prevent. The sync retries; a duplicate is forever.
				return "", err
			}
			if live {
				return id, nil
			}
			// Trashed, moved, or gone: the mapping is stale, so drop it and
			// fall through to recreating the folder where the path says it
			// belongs.
			fc.deps.persist.DeleteFolder(dir)
		}
	}

	id, err := fc.deps.create(ctx, parentID, stdpath.Base(dir))
	if err != nil {
		return "", err
	}
	if fc.deps.persist != nil {
		fc.deps.persist.SetFolder(dir, id)
	}
	return id, nil
}
