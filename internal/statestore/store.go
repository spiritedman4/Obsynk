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
	if m.Records == nil {
		m.Records = make(map[string]model.SyncRecord)
	}
	s.manifest = m
	return s, nil
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
