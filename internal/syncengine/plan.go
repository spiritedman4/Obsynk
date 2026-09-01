package syncengine

import (
	"sort"
	"strings"

	"obsynk/internal/model"
)

// Operation is one concrete action the executor must carry out, derived
// from a DiffResult that wasn't DecisionNone.
type Operation struct {
	Kind         Decision
	RelativePath string

	Local    *model.FileMetadata
	Remote   *RemoteEntry
	Baseline *model.SyncRecord

	// Winner is "local" or "remote", set only when Kind == DecisionConflict.
	Winner string
}

// Plan converts DiffResults into Operations, dropping DecisionNone entries
// (nothing to do) and ordering shallower paths before deeper ones so that,
// e.g., a push into a brand-new subdirectory is ordered after any operation
// for a shallower path that might create part of that directory structure.
// Ordering is about predictability, not correctness: ops run concurrently,
// so it guarantees nothing about which directory exists when. Making
// directory creation safe under that concurrency is folderCache's job.
func Plan(results []DiffResult) []Operation {
	ops := make([]Operation, 0, len(results))
	for _, r := range results {
		if r.Decision == DecisionNone {
			continue
		}
		ops = append(ops, Operation{
			Kind:         r.Decision,
			RelativePath: r.RelativePath,
			Local:        r.Local,
			Remote:       r.Remote,
			Baseline:     r.Baseline,
			Winner:       r.Winner,
		})
	}

	sort.Slice(ops, func(i, j int) bool {
		di, dj := pathDepth(ops[i].RelativePath), pathDepth(ops[j].RelativePath)
		if di != dj {
			return di < dj
		}
		return ops[i].RelativePath < ops[j].RelativePath
	})

	return ops
}

func pathDepth(relPath string) int {
	return strings.Count(relPath, "/")
}
