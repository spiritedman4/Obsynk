package model

import "time"

// SyncRecord is the last-synced baseline for one relative path: the content
// both the local vault and Google Drive agreed on as of the last successful
// sync. syncengine.Diff compares the current local scan and current Drive
// listing against this baseline to tell "changed locally" apart from
// "changed remotely" apart from "changed both."
type SyncRecord struct {
	RelativePath string

	LocalHash    string // hash at last successful sync
	LocalModTime time.Time

	DriveFileID  string
	DriveMD5     string // Drive's md5Checksum at last sync
	DriveModTime time.Time
	// DriveParentID is the Drive folder ID this file lived in as of last
	// sync, kept so a later rename/move can remove the correct old parent
	// via Drive's RemoveParents rather than leaving the file
	// multi-parented.
	DriveParentID string

	// LastSyncedHash equals LocalHash at the moment of the last sync; kept
	// as its own field so the intent (content both sides agreed on) reads
	// clearly at call sites, independent of which side originated it.
	LastSyncedHash string

	// Deleted marks a tombstone: the path was deleted locally and trashed
	// remotely. Retained (rather than removed outright) to distinguish a
	// genuine resurrection from a fresh, unrelated re-creation at the same
	// path.
	Deleted bool
}

// ManifestVersion is bumped whenever the on-disk Manifest schema changes in
// a way that requires migration.
const ManifestVersion = 1

// Manifest is the full persisted sync state for one vault.
type Manifest struct {
	Version     int                   `json:"version"`
	VaultPath   string                `json:"vaultPath"`
	DriveRootID string                `json:"driveRootId"`
	Records     map[string]SyncRecord `json:"records"` // key = RelativePath
}

// NewManifest returns an empty Manifest for vaultPath, ready to have
// Records populated.
func NewManifest(vaultPath string) Manifest {
	return Manifest{
		Version:   ManifestVersion,
		VaultPath: vaultPath,
		Records:   make(map[string]SyncRecord),
	}
}
