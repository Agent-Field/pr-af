// Package diffengine ports src/pr_af/diff_engine.py — the deterministic
// (non-LLM) diff parser and clustering engine that feeds the anatomy phase.
//
// It is a byte-for-byte behavioral port of the Python module. Where the Python
// implementation has quirks (see notes below) they are reproduced exactly
// rather than "fixed", because downstream golden comparisons and cluster/finding
// ordering depend on identical output:
//
//   - parse_unified_diff never emits a "renamed" status. Git rename headers
//     (`rename from`/`rename to`, `similarity index`) are not special-cased; a
//     rename surfaces as status "modified" with the path taken from the
//     `+++ b/` line (or "" for a pure content-free rename that has no `+++ b/`).
//   - A deleted file (`+++ /dev/null`) never sets a path, so its Path is "".
//   - Binary files (no `---`/`+++` lines) yield a FileChange with Path "" and no
//     hunks.
//   - Line splitting matches Python's str.splitlines() (not strings.Split on
//     "\n"): all Unicode line boundaries split, and a trailing line break does
//     NOT produce a final empty element. This keeps hunk `content` byte-exact.
package diffengine

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// hunkRe mirrors the Python regex `@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)`
// used with re.match (anchored at start). The trailing `(.*)` group is captured
// but unused in Python, so it is dropped here — its presence never changes
// whether the pattern matches.
var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// fileAccum is the mutable per-file accumulator, mirroring the Python
// `current_file` dict.
type fileAccum struct {
	path    string
	status  string
	hunks   []*hunkAccum
	added   int
	removed int
}

// hunkAccum is the mutable per-hunk accumulator, mirroring `current_hunk`.
type hunkAccum struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	header   string
	lines    []string
}

// ParseUnifiedDiff parses unified diff text into structured FileChange objects.
// It is a direct port of Python parse_unified_diff, preserving branch order and
// the accumulation quirks documented on the package.
func ParseUnifiedDiff(diffText string) []schemas.FileChange {
	files := make([]schemas.FileChange, 0)
	var currentFile *fileAccum
	var currentHunk *hunkAccum

	for _, line := range splitLines(diffText) {
		switch {
		// New file header — note: no `and current_file` guard in Python, this
		// branch fires for any line starting with "diff --git".
		case strings.HasPrefix(line, "diff --git"):
			if currentFile != nil {
				files = append(files, buildFileChange(currentFile))
			}
			currentFile = &fileAccum{status: "modified"}
			currentHunk = nil

		case strings.HasPrefix(line, "--- a/") && currentFile != nil:
			// old file path (handled by the +++ line) — intentionally ignored.

		case strings.HasPrefix(line, "--- /dev/null") && currentFile != nil:
			currentFile.status = "added"

		case strings.HasPrefix(line, "+++ b/") && currentFile != nil:
			currentFile.path = line[6:]

		case strings.HasPrefix(line, "+++ /dev/null") && currentFile != nil:
			currentFile.status = "removed"

		case strings.HasPrefix(line, "@@") && currentFile != nil:
			if m := hunkRe.FindStringSubmatch(line); m != nil {
				currentHunk = &hunkAccum{
					oldStart: atoiOr(m[1], 0),
					oldCount: atoiOr(m[2], 1),
					newStart: atoiOr(m[3], 0),
					newCount: atoiOr(m[4], 1),
					header:   line,
					lines:    []string{},
				}
				currentFile.hunks = append(currentFile.hunks, currentHunk)
			}
			// A malformed "@@" line (no regex match) is dropped: current_hunk is
			// left unchanged and the line is not accumulated — matching Python.

		case currentHunk != nil && currentFile != nil:
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				currentFile.added++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				currentFile.removed++
			}
			currentHunk.lines = append(currentHunk.lines, line)
		}
	}

	if currentFile != nil {
		files = append(files, buildFileChange(currentFile))
	}

	return files
}

// buildFileChange ports _build_file_change: joins each hunk's lines with "\n"
// and resolves the language from the path.
func buildFileChange(raw *fileAccum) schemas.FileChange {
	hunks := make([]schemas.Hunk, 0, len(raw.hunks))
	for _, h := range raw.hunks {
		hunks = append(hunks, schemas.Hunk{
			OldStart: h.oldStart,
			OldCount: h.oldCount,
			NewStart: h.newStart,
			NewCount: h.newCount,
			Header:   h.header,
			Content:  strings.Join(h.lines, "\n"),
		})
	}
	return schemas.FileChange{
		Path:         raw.path,
		Status:       raw.status,
		Language:     detectLanguage(raw.path),
		LinesAdded:   raw.added,
		LinesRemoved: raw.removed,
		Hunks:        hunks,
	}
}

// atoiOr returns the integer value of s, or def when s is empty. The regex
// guarantees s is either empty or all digits, so a parse error is impossible;
// def is returned defensively if one ever occurs.
func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// extLang is one (extension, language) mapping. A slice preserves Python's dict
// insertion order, which the "first suffix match wins" lookup depends on (Go
// map iteration order is randomized and would break parity).
type extLang struct {
	ext  string
	lang string
}

// langTable mirrors the Python _detect_language ext_map, in declaration order.
var langTable = []extLang{
	{".py", "python"},
	{".js", "javascript"},
	{".ts", "typescript"},
	{".tsx", "typescript"},
	{".jsx", "javascript"},
	{".go", "go"},
	{".rs", "rust"},
	{".java", "java"},
	{".rb", "ruby"},
	{".cpp", "cpp"},
	{".c", "c"},
	{".cs", "csharp"},
	{".swift", "swift"},
	{".kt", "kotlin"},
	{".scala", "scala"},
	{".php", "php"},
	{".sh", "bash"},
	{".yaml", "yaml"},
	{".yml", "yaml"},
	{".json", "json"},
	{".md", "markdown"},
	{".sql", "sql"},
	{".html", "html"},
	{".css", "css"},
}

// detectLanguage ports _detect_language: returns the language for the first
// table extension the path ends with, or "" if none match.
func detectLanguage(path string) string {
	for _, e := range langTable {
		if strings.HasSuffix(path, e.ext) {
			return e.lang
		}
	}
	return ""
}

// splitLines reproduces Python's str.splitlines(): it splits on every Unicode
// line boundary CPython recognizes, treats "\r\n" as a single boundary, and —
// crucially — does NOT emit a trailing empty element when the text ends with a
// line break. This keeps hunk content byte-identical to the Python port.
func splitLines(s string) []string {
	var lines []string
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		c := runes[i]
		if isLineBoundary(c) {
			lines = append(lines, b.String())
			b.Reset()
			if c == '\r' && i+1 < len(runes) && runes[i+1] == '\n' {
				i += 2
			} else {
				i++
			}
			continue
		}
		b.WriteRune(c)
		i++
	}
	if b.Len() > 0 {
		lines = append(lines, b.String())
	}
	return lines
}

// isLineBoundary reports whether r is one of the line boundaries recognized by
// Python's str.splitlines().
func isLineBoundary(r rune) bool {
	switch r {
	case '\n', // Line Feed
		'\r',   // Carriage Return
		'\v',   // Line Tabulation (0x0b)
		'\f',   // Form Feed (0x0c)
		0x1c,   // File Separator
		0x1d,   // Group Separator
		0x1e,   // Record Separator
		0x85,   // Next Line (C1)
		0x2028, // Line Separator
		0x2029: // Paragraph Separator
		return true
	}
	return false
}
