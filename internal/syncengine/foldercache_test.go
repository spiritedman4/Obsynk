package syncengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeFolder is one folder as it actually exists on the fake Drive,
// addressable by ID regardless of what the search index currently shows.
type fakeFolder struct {
	name    string
	parent  string
	trashed bool
}

// fakeDrive models the two Drive calls folderCache makes, and -- crucially
// -- the fact that they read different things. EnsureChildFolder searches
// an index that lags behind writes; VerifyFolder is a get-by-ID that
// doesn't.
type fakeDrive struct {
	mu sync.Mutex

	byID    map[string]*fakeFolder       // the truth, as get-by-ID sees it
	index   map[string]map[string]string // parentID -> name -> ID, as search sees it
	creates map[string]int               // "parentID/name" -> times created
	seq     int

	// blindSearch makes every search miss, standing in for Drive's index
	// not yet reflecting a folder that was created moments ago.
	blindSearch bool
	// failures injects transient create failures, keyed "parentID/name".
	failures map[string]int
	// verifyErr, when set, makes verification fail outright (a network
	// error, say) rather than reporting a folder present or absent.
	verifyErr error
	verifies  int
}

func newFakeDrive() *fakeDrive {
	return &fakeDrive{
		byID:     make(map[string]*fakeFolder),
		index:    make(map[string]map[string]string),
		creates:  make(map[string]int),
		failures: make(map[string]int),
	}
}

func (d *fakeDrive) ensureChild(_ context.Context, parentID, name string) (string, error) {
	key := parentID + "/" + name

	d.mu.Lock()
	if !d.blindSearch {
		if id, ok := d.index[parentID][name]; ok {
			d.mu.Unlock()
			return id, nil
		}
	}
	if d.failures[key] > 0 {
		d.failures[key]--
		d.mu.Unlock()
		return "", errors.New("drive: transient failure")
	}
	d.mu.Unlock()

	// The list-to-create window that concurrent callers used to race in.
	time.Sleep(5 * time.Millisecond)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	id := fmt.Sprintf("folder-%d", d.seq)
	d.byID[id] = &fakeFolder{name: name, parent: parentID}
	if d.index[parentID] == nil {
		d.index[parentID] = make(map[string]string)
	}
	// Last writer wins, mirroring Drive handing back an arbitrary one of
	// several same-named folders on a later search.
	d.index[parentID][name] = id
	d.creates[key]++
	return id, nil
}

func (d *fakeDrive) verifyFolder(_ context.Context, folderID, parentID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.verifies++
	if d.verifyErr != nil {
		return false, d.verifyErr
	}
	f, ok := d.byID[folderID]
	if !ok || f.trashed {
		return false, nil
	}
	return parentID == "" || f.parent == parentID, nil
}

func (d *fakeDrive) createCount(parentID, name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.creates[parentID+"/"+name]
}

// trash marks a folder deleted on Drive, as a user emptying it into the bin
// would, leaving any remembered ID pointing at it stale.
func (d *fakeDrive) trash(folderID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.byID[folderID]
	f.trashed = true
	delete(d.index[f.parent], f.name)
}

// move reparents a folder, as dragging it elsewhere in Drive would.
func (d *fakeDrive) move(folderID, newParent string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.byID[folderID]
	delete(d.index[f.parent], f.name)
	f.parent = newParent
	if d.index[newParent] == nil {
		d.index[newParent] = make(map[string]string)
	}
	d.index[newParent][f.name] = folderID
}

// fakePersister stands in for the statestore's folder map.
type fakePersister struct {
	mu      sync.Mutex
	folders map[string]string
	deletes int
}

func newFakePersister(seed map[string]string) *fakePersister {
	p := &fakePersister{folders: make(map[string]string)}
	for k, v := range seed {
		p.folders[k] = v
	}
	return p
}

func (p *fakePersister) FolderID(dir string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.folders[dir]
	return id, ok
}

func (p *fakePersister) SetFolder(dir, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.folders[dir] = id
}

func (p *fakePersister) DeleteFolder(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.folders, dir)
	p.deletes++
}

func (p *fakePersister) get(dir string) string {
	id, _ := p.FolderID(dir)
	return id
}

// searchOnly builds deps with no cross-sync persistence, for the tests that
// only care about within-batch behavior.
func searchOnly(d *fakeDrive) folderDeps {
	return folderDeps{create: d.ensureChild}
}

func withPersistence(d *fakeDrive, p *fakePersister) folderDeps {
	return folderDeps{create: d.ensureChild, verify: d.verifyFolder, persist: p}
}

// resolveAll resolves every dir concurrently and returns the resulting IDs
// in the same order.
func resolveAll(t *testing.T, fc *folderCache, dirs []string) []string {
	t.Helper()

	ids := make([]string, len(dirs))
	errs := make([]error, len(dirs))
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			<-start
			ids[i], errs[i] = fc.resolve(context.Background(), dir)
		}(i, dir)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("resolve(%q): %v", dirs[i], err)
		}
	}
	return ids
}

func TestFolderCacheCreatesEachDirectoryOnceUnderConcurrency(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	fc := newFolderCache("root", nil, searchOnly(d))

	// Eight files landing in the same brand-new folder, one per worker --
	// the shape of a first full sync, which used to leave one Drive folder
	// per worker with the files split between them.
	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i] = "AI & ML"
	}

	ids := resolveAll(t, fc, dirs)

	if got := d.createCount("root", "AI & ML"); got != 1 {
		t.Errorf("created %q %d times, want 1 (duplicate folders on Drive)", "AI & ML", got)
	}
	for i, id := range ids {
		if id != ids[0] {
			t.Errorf("resolve #%d returned folder %q, want %q -- files would be split across folders", i, id, ids[0])
		}
	}
}

func TestFolderCacheCreatesSharedAncestorOnce(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	fc := newFolderCache("root", nil, searchOnly(d))

	dirs := []string{"AI & ML/papers", "AI & ML/notes", "AI & ML/notes", "AI & ML/papers/2026", "Postgres"}
	ids := resolveAll(t, fc, dirs)

	if got := d.createCount("root", "AI & ML"); got != 1 {
		t.Errorf("created shared ancestor %d times, want 1", got)
	}
	for _, want := range []struct{ parent, name string }{
		{ids[0], "2026"}, {"root", "Postgres"},
	} {
		if got := d.createCount(want.parent, want.name); got != 1 {
			t.Errorf("created %q under %q %d times, want 1", want.name, want.parent, got)
		}
	}
	if ids[1] != ids[2] {
		t.Errorf("same dir resolved to %q and %q", ids[1], ids[2])
	}
	if got := d.createCount("root", "AI & ML/papers"); got != 0 {
		t.Errorf("resolved a nested path as one folder name")
	}
}

func TestFolderCacheRootAndSeededDirsSkipCreation(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	fc := newFolderCache("root", map[string]string{"Docker": "existing-docker"}, searchOnly(d))

	for _, dir := range []string{"", "."} {
		id, err := fc.resolve(context.Background(), dir)
		if err != nil {
			t.Fatalf("resolve(%q): %v", dir, err)
		}
		if id != "root" {
			t.Errorf("resolve(%q) = %q, want root", dir, id)
		}
	}

	id, err := fc.resolve(context.Background(), "Docker")
	if err != nil {
		t.Fatalf("resolve(Docker): %v", err)
	}
	if id != "existing-docker" {
		t.Errorf("resolve(Docker) = %q, want the seeded ID", id)
	}
	if got := d.createCount("root", "Docker"); got != 0 {
		t.Errorf("created a seeded folder %d times, want 0", got)
	}
}

func TestFolderCacheDoesNotCacheFailures(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	d.failures["root/Golang"] = 1
	fc := newFolderCache("root", nil, searchOnly(d))

	if _, err := fc.resolve(context.Background(), "Golang"); err == nil {
		t.Fatal("expected the injected failure to surface")
	}

	id, err := fc.resolve(context.Background(), "Golang")
	if err != nil {
		t.Fatalf("retry after a transient failure: %v", err)
	}
	if id == "" {
		t.Error("retry returned an empty folder ID")
	}
}

func TestFolderCacheResolveHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	blocking := func(_ context.Context, _, _ string) (string, error) {
		once.Do(func() { close(started) })
		<-release
		return "folder-1", nil
	}
	fc := newFolderCache("root", nil, folderDeps{create: blocking})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		fc.resolve(context.Background(), "Linux") //nolint:errcheck // holds the entry in flight
	}()

	// The entry is registered before create runs, so once create has been
	// entered the next resolve is necessarily a waiter, not the owner.
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := fc.resolve(ctx, "Linux")
		waiterDone <- err
	}()

	cancel()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("a waiting resolve ignored context cancellation")
	}

	close(release)
	wg.Wait()
}

// The cross-sync case: sync one creates the folder, sync two gets a fresh
// cache and a search index that hasn't caught up yet. Without the
// remembered ID, sync two searches, finds nothing, and creates a duplicate.
func TestFolderCacheReusesRememberedIDWhenSearchIndexLags(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	p := newFakePersister(nil)

	first := newFolderCache("root", nil, withPersistence(d, p))
	id, err := first.resolve(context.Background(), "AI & ML")
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := p.get("AI & ML"); got != id {
		t.Fatalf("first sync remembered %q, want %q", got, id)
	}

	// Sync two, seconds later: new cache, and Drive's search still can't
	// see the folder created a moment ago.
	d.blindSearch = true
	second := newFolderCache("root", nil, withPersistence(d, p))
	got, err := second.resolve(context.Background(), "AI & ML")
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if got != id {
		t.Errorf("second sync resolved to %q, want the remembered %q", got, id)
	}
	if n := d.createCount("root", "AI & ML"); n != 1 {
		t.Errorf("created %q %d times across two syncs, want 1", "AI & ML", n)
	}
}

func TestFolderCacheRecreatesWhenRememberedFolderIsStale(t *testing.T) {
	t.Parallel()

	staleWays := map[string]func(d *fakeDrive, id string){
		"trashed":        func(d *fakeDrive, id string) { d.trash(id) },
		"movedElsewhere": func(d *fakeDrive, id string) { d.move(id, "somewhere-else") },
		"gone": func(d *fakeDrive, id string) {
			d.mu.Lock()
			defer d.mu.Unlock()
			f := d.byID[id]
			delete(d.byID, id)
			delete(d.index[f.parent], f.name)
		},
	}

	for name, makeStale := range staleWays {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := newFakeDrive()
			p := newFakePersister(nil)

			first := newFolderCache("root", nil, withPersistence(d, p))
			id, err := first.resolve(context.Background(), "Postgres")
			if err != nil {
				t.Fatalf("first sync: %v", err)
			}

			makeStale(d, id)

			second := newFolderCache("root", nil, withPersistence(d, p))
			got, err := second.resolve(context.Background(), "Postgres")
			if err != nil {
				t.Fatalf("second sync: %v", err)
			}

			if got == id {
				t.Errorf("reused stale folder %q -- uploads would land somewhere the vault path doesn't point", id)
			}
			if n := d.createCount("root", "Postgres"); n != 2 {
				t.Errorf("created %q %d times, want 2 (once, then again after it went stale)", "Postgres", n)
			}
			if p.get("Postgres") != got {
				t.Errorf("remembered %q after recreating as %q", p.get("Postgres"), got)
			}
			if p.deletes != 1 {
				t.Errorf("dropped the stale mapping %d times, want 1", p.deletes)
			}
		})
	}
}

// A verification that can't complete says nothing about whether the folder
// is there. Guessing "absent" is what creates duplicates, so the operation
// must fail instead.
func TestFolderCacheFailsRatherThanGuessingWhenVerifyErrors(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	p := newFakePersister(map[string]string{"BigData": "folder-known"})
	d.verifyErr = errors.New("drive: network unreachable")

	fc := newFolderCache("root", nil, withPersistence(d, p))
	if _, err := fc.resolve(context.Background(), "BigData"); err == nil {
		t.Fatal("expected the verification failure to surface")
	}
	if n := d.createCount("root", "BigData"); n != 0 {
		t.Errorf("created a folder %d times after a failed check, want 0", n)
	}
	if p.get("BigData") != "folder-known" {
		t.Error("dropped a remembered mapping on a failed check, rather than leaving it for the retry")
	}
}

func TestFolderCacheRemembersSeededDirs(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	p := newFakePersister(nil)

	// A full sync seeds from its Drive tree listing; those IDs are the
	// freshest available, so they should refresh what's remembered.
	newFolderCache("root", map[string]string{"Linux": "folder-linux", "Linux/kernel": "folder-kernel"}, withPersistence(d, p))

	if p.get("Linux") != "folder-linux" || p.get("Linux/kernel") != "folder-kernel" {
		t.Errorf("seeded dirs not remembered: %v", p.folders)
	}
	if d.verifies != 0 {
		t.Errorf("verified %d seeded dirs, want 0 -- a listing from this same sync is already fresh", d.verifies)
	}
}

func TestFolderCacheVerifiesRememberedDirOnlyOncePerBatch(t *testing.T) {
	t.Parallel()

	d := newFakeDrive()
	p := newFakePersister(nil)

	first := newFolderCache("root", nil, withPersistence(d, p))
	if _, err := first.resolve(context.Background(), "Python"); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	second := newFolderCache("root", nil, withPersistence(d, p))
	dirs := make([]string, 6)
	for i := range dirs {
		dirs[i] = "Python"
	}
	resolveAll(t, second, dirs)

	if d.verifies != 1 {
		t.Errorf("verified %d times for 6 files in one directory, want 1", d.verifies)
	}
}
