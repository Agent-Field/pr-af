package diffengine

import (
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// testPatterns ports the Python _is_test_file patterns. Membership is a
// case-insensitive SUBSTRING test (not a path-component test), so e.g.
// "latest/foo.py" matches on "test/" — a Python quirk reproduced verbatim.
var testPatterns = []string{
	"test_",
	"_test.",
	".test.",
	"tests/",
	"test/",
	"__tests__/",
	"spec/",
}

// isTestFile ports _is_test_file: true when the lowercased path contains any
// test pattern as a substring.
func isTestFile(path string) bool {
	lower := strings.ToLower(path)
	for _, p := range testPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// ComputeDiffStats ports compute_diff_stats: aggregate statistics over the
// parsed files. The test-to-code ratio divides the test-file count by
// max(code_files, 1) so it never divides by zero.
func ComputeDiffStats(files []schemas.FileChange) schemas.DiffStats {
	testFiles := 0
	totalAdditions := 0
	totalDeletions := 0
	filesAdded := 0
	filesModified := 0
	filesRemoved := 0
	filesRenamed := 0

	for _, f := range files {
		if isTestFile(f.Path) {
			testFiles++
		}
		totalAdditions += f.LinesAdded
		totalDeletions += f.LinesRemoved
		switch f.Status {
		case "added":
			filesAdded++
		case "modified":
			filesModified++
		case "removed":
			filesRemoved++
		case "renamed":
			filesRenamed++
		}
	}

	codeFiles := len(files) - testFiles
	denom := codeFiles
	if denom < 1 {
		denom = 1
	}

	return schemas.DiffStats{
		TotalFiles:       len(files),
		TotalAdditions:   totalAdditions,
		TotalDeletions:   totalDeletions,
		FilesAdded:       filesAdded,
		FilesModified:    filesModified,
		FilesRemoved:     filesRemoved,
		FilesRenamed:     filesRenamed,
		TestFilesChanged: testFiles,
		TestToCodeRatio:  float64(testFiles) / float64(denom),
	}
}
