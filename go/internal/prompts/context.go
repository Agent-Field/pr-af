package prompts

import "github.com/Agent-Field/pr-af/go/internal/schemas"

// Shared context-payload helpers. These mirror the small dict/list constructions
// the reasoners perform before json.dumps: _cluster_descriptions, stats
// model_dump, the files_changed projection, and the intake sub-object.

// clusterDescriptions ports _cluster_descriptions(clusters): a list of ordered
// objects keyed id, name, description, primary_language, files.
func clusterDescriptions(clusters []schemas.ChangeCluster) []*OMap {
	out := make([]*OMap, len(clusters))
	for i, c := range clusters {
		out[i] = omap(
			"id", c.ID,
			"name", c.Name,
			"description", c.Description,
			"primary_language", c.PrimaryLanguage,
			"files", orEmpty(c.Files),
		)
	}
	return out
}

// statsOMap ports DiffStats.model_dump() — field order fixed by the pydantic
// model. test_to_code_ratio is a float and renders with a decimal point.
func statsOMap(s schemas.DiffStats) *OMap {
	return omap(
		"total_files", s.TotalFiles,
		"total_additions", s.TotalAdditions,
		"total_deletions", s.TotalDeletions,
		"files_added", s.FilesAdded,
		"files_modified", s.FilesModified,
		"files_removed", s.FilesRemoved,
		"files_renamed", s.FilesRenamed,
		"test_files_changed", s.TestFilesChanged,
		"test_to_code_ratio", s.TestToCodeRatio,
	)
}

// filePaths ports [f.path for f in files[:30]].
func filePaths(files []schemas.FileChange) []string {
	files = firstN(files, 30)
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// fileChangeOMaps ports the anatomy files_changed projection over files[:30]:
// objects keyed path, status, lines_added, lines_removed.
func fileChangeOMaps(files []schemas.FileChange) []*OMap {
	files = firstN(files, 30)
	out := make([]*OMap, len(files))
	for i, f := range files {
		out[i] = omap(
			"path", f.Path,
			"status", f.Status,
			"lines_added", f.LinesAdded,
			"lines_removed", f.LinesRemoved,
		)
	}
	return out
}
