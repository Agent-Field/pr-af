package diffengine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// ClusterChanges ports cluster_changes: group files by their directory (the
// path up to the last "/"; "root" when there is no "/"), then emit one cluster
// per directory in sorted-directory order with deterministic ids "cluster_0",
// "cluster_1", ... The primary language is the most frequent language in the
// group.
//
// Divergence note: Python computes primary_language via
// `max(set(langs), key=langs.count)`, whose tie-break depends on set iteration
// order and is therefore non-deterministic across Python processes (str hash
// randomization). This port instead breaks ties deterministically by
// first-appearance order within the group. For a unique mode (the overwhelming
// common case, including single-language groups) both agree exactly.
func ClusterChanges(files []schemas.FileChange) []schemas.ChangeCluster {
	// Group by directory. Go maps do not preserve insertion order, so the
	// directory keys are sorted explicitly below to match Python's
	// sorted(dir_groups.items()). Per-group file order is preserved by append.
	groups := map[string][]schemas.FileChange{}
	for _, f := range files {
		directory := "root"
		if idx := strings.LastIndex(f.Path, "/"); idx >= 0 {
			directory = f.Path[:idx]
		}
		groups[directory] = append(groups[directory], f)
	}

	dirs := make([]string, 0, len(groups))
	for d := range groups {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	clusters := make([]schemas.ChangeCluster, 0, len(dirs))
	for i, directory := range dirs {
		groupFiles := groups[directory]
		paths := make([]string, 0, len(groupFiles))
		for _, f := range groupFiles {
			paths = append(paths, f.Path)
		}
		clusters = append(clusters, schemas.ChangeCluster{
			ID:              fmt.Sprintf("cluster_%d", i),
			Name:            directory,
			Files:           paths,
			PrimaryLanguage: primaryLanguage(groupFiles),
		})
	}
	return clusters
}

// primaryLanguage returns the most frequent non-empty language among the files,
// breaking count ties by first appearance in the group (deterministic).
func primaryLanguage(files []schemas.FileChange) string {
	langs := make([]string, 0, len(files))
	for _, f := range files {
		if f.Language != "" {
			langs = append(langs, f.Language)
		}
	}
	if len(langs) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, l := range langs {
		counts[l]++
	}
	best := ""
	bestCount := -1
	for _, l := range langs {
		if counts[l] > bestCount {
			bestCount = counts[l]
			best = l
		}
	}
	return best
}
