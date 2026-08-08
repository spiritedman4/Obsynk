package model

import "time"

// FileMetadata describes one file (or directory) encountered while scanning
// a vault.
type FileMetadata struct {
	// Path is the absolute filesystem path.
	Path string
	// RelativePath is vault-root-relative, forward-slash normalized, and
	// used as the sync key throughout statestore/syncengine.
	RelativePath string
	Name         string
	IsDir        bool
	Size         int64
	ModifiedTime time.Time
	// Hash is the sha256 hex digest of the file's contents; empty for
	// directories.
	Hash string
}
