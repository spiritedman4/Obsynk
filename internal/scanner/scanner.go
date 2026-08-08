// Package scanner walks an Obsidian vault concurrently, hashing every
// regular file so the sync engine can diff local state against the last
// synced baseline and Google Drive.
package scanner

import (
	"context"
	"io/fs"
	"os"
	stdpath "path"
	"path/filepath"
	"runtime"
	"strings"

	"obsynk/internal/model"
)

// DefaultIgnoreGlobs excludes paths that must never be scanned or synced:
// Obsidian's own config dir (holds this plugin's manifest.json/token.json),
// git metadata, and Obsidian's local trash.
var DefaultIgnoreGlobs = []string{
	".obsidian/**",
	".git/**",
	".trash/**",
}

type Options struct {
	VaultRoot   string
	IgnoreGlobs []string
	// Workers bounds hashing concurrency; 0 selects min(NumCPU, 8).
	Workers int
}

type Result struct {
	Meta model.FileMetadata
	Err  error
}

// Scan walks VaultRoot concurrently, hashing every non-ignored regular file,
// and streams results on the returned channel. The channel is closed once
// the walk and all hashing workers finish. Cancelling ctx stops further
// dispatch; workers already hashing a file finish that one file first.
func Scan(ctx context.Context, opts Options) (<-chan Result, error) {
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
		if workers > 8 {
			workers = 8
		}
		if workers < 1 {
			workers = 1
		}
	}

	root := filepath.Clean(opts.VaultRoot)

	paths := make(chan string, workers*2)
	results := make(chan Result, workers*2)

	done := make(chan struct{})

	for i := 0; i < workers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for path := range paths {
				meta, err := statAndHash(root, path)
				select {
				case results <- Result{Meta: meta, Err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(paths)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				select {
				case results <- Result{Err: walkErr}:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			}

			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}

			if matchIgnore(rel, opts.IgnoreGlobs) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}

			if d.IsDir() {
				return nil
			}

			select {
			case paths <- path:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
		if err != nil && err != context.Canceled {
			select {
			case results <- Result{Err: err}:
			case <-ctx.Done():
			}
		}
	}()

	go func() {
		for i := 0; i < workers; i++ {
			<-done
		}
		close(results)
	}()

	return results, nil
}

func statAndHash(root, path string) (model.FileMetadata, error) {
	info, err := os.Stat(path)
	if err != nil {
		return model.FileMetadata{}, err
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return model.FileMetadata{}, err
	}
	rel = filepath.ToSlash(rel)

	hash, err := HashFile(path)
	if err != nil {
		return model.FileMetadata{}, err
	}

	return model.FileMetadata{
		Path:         path,
		RelativePath: rel,
		Name:         info.Name(),
		IsDir:        false,
		Size:         info.Size(),
		ModifiedTime: info.ModTime(),
		Hash:         hash,
	}, nil
}

// IsIgnored reports whether relPath (forward-slash normalized) should be
// excluded from scanning/syncing, per the given glob patterns (see
// DefaultIgnoreGlobs). Exported so callers scanning a single path directly
// (e.g. syncengine's per-event sync, which skips a full Scan) can apply the
// same exclusion rules.
func IsIgnored(relPath string, patterns []string) bool {
	return matchIgnore(relPath, patterns)
}

// matchIgnore reports whether relPath (forward-slash normalized) matches
// any of the given glob patterns. A pattern ending in "/**" matches the
// exact prefix and everything under it; other patterns are matched with
// path.Match against the whole relative path.
func matchIgnore(relPath string, patterns []string) bool {
	for _, p := range patterns {
		p = filepath.ToSlash(p)
		if prefix, ok := strings.CutSuffix(p, "/**"); ok {
			if relPath == prefix || strings.HasPrefix(relPath, prefix+"/") {
				return true
			}
			continue
		}
		if ok, _ := stdpath.Match(p, relPath); ok {
			return true
		}
	}
	return false
}
