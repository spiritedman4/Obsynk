// Package statestore persists the last-synced baseline manifest for a
// vault: a JSON file loaded into memory and mutated in place, saved back via
// an atomic temp-file-then-rename write. Chosen over an embedded KV store
// (e.g. bbolt) because a vault's manifest is small (hundreds to low
// thousands of records) and the daemon's access pattern is load-once,
// mutate-a-batch-per-sync, save — not a workload that benefits from a
// transactional B+tree, while JSON stays trivially human-inspectable for
// debugging.
package statestore

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"obsynk/internal/model"
)

type Store struct {
	mu       sync.RWMutex
	path     string
	manifest model.Manifest
}

// Open loads the manifest at path, or starts a fresh empty one for
// vaultPath if no file exists yet.
func Open(path, vaultPath string) (*Store, error) {
	s := &Store{path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.manifest = model.NewManifest(vaultPath)
			return s, nil
		}
		return nil, err
	}

	var m model.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if err := migrate(&m); err != nil {
		return nil, err
	}
	s.manifest = m
	return s, nil
}

// migrate brings a manifest loaded from disk up to ManifestVersion. It
// refuses one written by a newer daemon rather than silently dropping
// fields it doesn't know about when it saves the file back.
func migrate(m *model.Manifest) error {
	if m.Version > model.ManifestVersion {
		return fmt.Errorf("statestore: manifest version %d was written by a newer version of obsynk (this build understands %d)", m.Version, model.ManifestVersion)
	}
	if m.Records == nil {
		m.Records = make(map[string]model.SyncRecord)
	}
	// v1 -> v2: Folders is new, and starts empty. Nothing is lost -- an
	// unknown directory is resolved by search on first use, exactly as
	// before, and remembered from then on.
	if m.Folders == nil {
		m.Folders = make(map[string]string)
	}
	m.Version = model.ManifestVersion
	return nil
}

func (s *Store) Get(relPath string) (model.SyncRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.manifest.Records[relPath]
	return rec, ok
}

func (s *Store) Set(rec model.SyncRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest.Records[rec.RelativePath] = rec
}

func (s *Store) Delete(relPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.manifest.Records, relPath)
}

// Snapshot returns a copy of the current records, safe for a caller to
// range over while diffing without holding the store's lock.
func (s *Store) Snapshot() map[string]model.SyncRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]model.SyncRecord, len(s.manifest.Records))
	for k, v := range s.manifest.Records {
		out[k] = v
	}
	return out
}

// FolderID returns the remembered Drive folder ID for a vault-relative
// directory path. The caller must verify it's still live before trusting
// it -- the folder may have been trashed or moved on Drive since.
func (s *Store) FolderID(dir string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.manifest.Folders[dir]
	return id, ok
}

// SetFolder remembers dir's Drive folder ID for later syncs.
func (s *Store) SetFolder(dir, id string) {
	if dir == "" || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest.Folders[dir] = id
}

// DeleteFolder forgets dir's remembered folder ID, called when the stored
// ID turns out to be stale.
func (s *Store) DeleteFolder(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.manifest.Folders, dir)
}

// Save persists the current manifest atomically.
func (s *Store) Save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.manifest, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return atomicWriteFile(s.path, data, 0o600)
}

// RootAdoption is the outcome of reconciling a manifest against the Drive
// root folder the daemon is configured to sync with.
type RootAdoption int

const (
	// RootUnchanged means the manifest already describes this root.
	RootUnchanged RootAdoption = iota
	// RootRecorded means the manifest had no root recorded and has now
	// adopted this one, keeping its records. This is the first run, or a
	// manifest written before the root was tracked.
	RootRecorded
	// RootReset means the manifest described a different root, so its
	// baseline was discarded.
	RootReset
)

// AdoptRoot reconciles the manifest with the Drive root folder the daemon
// is configured to use, discarding the baseline if it describes a different
// folder.
//
// A baseline record is not a description of the vault; it's a description
// of an agreement between the vault and one specific Drive folder tree
// ("note.md matched Drive file X in parent Y"). Point the daemon at a
// different root and every one of those claims is about files that have
// nothing to do with the new folder -- pushes would keep updating the old
// folder's files in place, and a full sync would read the new folder's
// emptiness as "everything was deleted on Drive" and delete the local vault
// to match. Dropping the baseline instead makes the next sync treat every
// path as new and upload it into the new root.
//
// An empty recorded root is adopted rather than treated as a change: it
// means "not tracked yet", which is what every manifest written before this
// was tracked looks like, and wiping those would be destructive on upgrade.
//
// The caller must Save to persist the outcome.
func (s *Store) AdoptRoot(rootID string) RootAdoption {
	if rootID == "" {
		return RootUnchanged
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.manifest.DriveRootID {
	case rootID:
		return RootUnchanged
	case "":
		s.manifest.DriveRootID = rootID
		return RootRecorded
	default:
		s.manifest.DriveRootID = rootID
		s.manifest.Records = make(map[string]model.SyncRecord)
		s.manifest.Folders = make(map[string]string)
		return RootReset
	}
}
