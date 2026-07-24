// Package evidence ports pr_af/evidence.py: ground-truth code extraction that
// grounds each finding in the actual repository source. It shells out to the
// same `grep` binary with the same arguments Python uses (so caller/import
// discovery is byte-identical regardless of the grep flavor installed), reads
// snippets around finding lines, and pre-reads a review dimension's target
// files into a primed context pack.
//
// Divergences from Python (all output-neutral or unreachable in practice):
//   - The Python (abspath, mtime) file-read cache (_FILE_CACHE) is a pure
//     performance optimization ("zero quality cost") and is NOT ported; files
//     are read fresh. Output is identical.
//   - Python opens files with errors="ignore" (dropping undecodable bytes); Go
//     reads raw bytes. This only differs on non-UTF-8 text files, where the
//     regex/line-number logic is unaffected in practice.
//   - _extract_diff_hunk's fallback scan over diff_patches follows Python's
//     dict INSERTION order to pick the first key that normalizes to the target;
//     Go maps have no order, so the scan uses SORTED key order. This only
//     matters when two distinct keys normalize identically with differing patch
//     values — a pathological input that does not occur for real diff maps.
package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sync/semaphore"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// EvidencePackage ports the pydantic EvidencePackage: ground-truth code
// evidence for a single finding. Slice fields default to non-nil empty slices
// (Field(default_factory=list)) so they marshal to [] rather than null.
type EvidencePackage struct {
	FindingTitle     string   `json:"finding_title"`
	PrimaryCode      string   `json:"primary_code"`
	CallerSnippets   []string `json:"caller_snippets"`
	CrossRefSnippets []string `json:"cross_ref_snippets"`
	DiffHunk         string   `json:"diff_hunk"`
	ImportContext    string   `json:"import_context"`
	RelatedCode      string   `json:"related_code"`
}

// evidenceSemaphoreWeight is asyncio.Semaphore(10) from
// extract_evidence_for_findings.
const evidenceSemaphoreWeight = 10

// extractForFindingFn is the per-finding worker, indirected through a package
// var so tests can observe concurrency (semaphore bound) and ordering without
// touching real files. Production behavior is identical to calling
// extractForFinding directly.
var extractForFindingFn = extractForFinding

// grepTimeout mirrors subprocess.run(..., timeout=10).
const grepTimeout = 10 * time.Second

var skipDirGrepArgs = []string{
	"--exclude-dir=.git",
	"--exclude-dir=node_modules",
	"--exclude-dir=__pycache__",
	"--exclude-dir=.venv",
	"--exclude-dir=vendor",
	"--exclude-dir=venv",
}

// textExtensions ports _TEXT_EXTENSIONS (extensions carry the leading dot).
var textExtensions = map[string]struct{}{
	".py": {}, ".js": {}, ".jsx": {}, ".ts": {}, ".tsx": {}, ".go": {}, ".rs": {},
	".java": {}, ".rb": {}, ".php": {}, ".c": {}, ".h": {}, ".cpp": {}, ".hpp": {},
	".cs": {}, ".swift": {}, ".kt": {}, ".scala": {}, ".sh": {}, ".yaml": {},
	".yml": {}, ".json": {}, ".toml": {}, ".ini": {}, ".cfg": {}, ".md": {},
	".sql": {}, ".html": {}, ".css": {}, ".scss": {}, ".txt": {},
}

// commonIdentifierWords ports _COMMON_IDENTIFIER_WORDS.
var commonIdentifierWords = map[string]struct{}{
	"the": {}, "this": {}, "that": {}, "with": {}, "from": {}, "when": {},
	"where": {}, "which": {}, "there": {}, "their": {}, "returns": {},
	"return": {}, "found": {}, "check": {}, "line": {}, "file": {}, "code": {},
	"issue": {}, "error": {}, "value": {}, "values": {}, "class": {},
	"function": {}, "method": {}, "should": {}, "could": {}, "would": {},
	"into": {}, "over": {}, "under": {}, "each": {}, "name": {}, "data": {},
	"test": {}, "tests": {},
}

var (
	identRe         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	backtickIdentRe = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`")
	capWordsRe      = regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]{2,})\b`)
	snakeCallRe     = regexp.MustCompile(`\b([a-z_][a-z0-9_]{2,})\s*\(`)
	backtickPathRe  = regexp.MustCompile("`([^`]*?/[^`]*?)`")
	pathLikeRe      = regexp.MustCompile(`([A-Za-z0-9_./-]+\.[A-Za-z0-9]+)`)
	hunkHeaderRe    = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
)

// ExtractEvidenceForFindings ports extract_evidence_for_findings. It processes
// findings concurrently, bounded by a weighted semaphore of 10, and returns a
// map keyed by finding title. Results are written into a PRE-INDEXED slice
// (never appended on completion) so that on a title collision the last finding
// in input order wins, exactly as Python's {p.finding_title: p for p in gather}.
func ExtractEvidenceForFindings(
	ctx context.Context,
	findings []schemas.ReviewFinding,
	repoPath string,
	diffPatches map[string]string,
	blastRadius []string,
) (map[string]EvidencePackage, error) {
	if len(findings) == 0 {
		return map[string]EvidencePackage{}, nil
	}

	sem := semaphore.NewWeighted(evidenceSemaphoreWeight)
	results := make([]EvidencePackage, len(findings))

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i := range findings {
		if err := sem.Acquire(ctx, 1); err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			break
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer sem.Release(1)
			results[idx] = extractForFindingFn(ctx, findings[idx], repoPath, diffPatches, blastRadius)
		}(i)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	out := make(map[string]EvidencePackage, len(results))
	for _, pkg := range results {
		out[pkg.FindingTitle] = pkg
	}
	return out, nil
}

// extractForFinding ports the inner _extract_for_finding closure. Python runs
// the sub-tasks concurrently via asyncio.to_thread + gather; because each
// sub-task writes a distinct, order-independent field (and the caller/cross-ref
// lists are assembled in the SAME input order gather preserves), running them
// sequentially here yields byte-identical results.
func extractForFinding(
	ctx context.Context,
	finding schemas.ReviewFinding,
	repoPath string,
	diffPatches map[string]string,
	blastRadius []string,
) EvidencePackage {
	normalizedFile := normalizeRelativePath(repoPath, finding.FilePath)
	textBlob := strings.Join([]string{finding.Title, finding.Body, finding.Evidence}, "\n")
	identifiers := extractMentionedIdentifiers(textBlob)

	primaryCode := readCodeSnippet(repoPath, normalizedFile, finding.LineStart, 30)
	diffHunk := extractDiffHunk(diffPatches, normalizedFile, &finding.LineStart)
	importContext := buildImportContext(ctx, repoPath, normalizedFile)
	mentionedFiles := extractMentionedFilePaths(textBlob, repoPath)
	relatedCode := extractBlastRadiusCode(repoPath, normalizedFile, identifiers, blastRadius)

	var callerGroups [][]string
	for _, ident := range identifiers {
		callerGroups = append(callerGroups, findFunctionCallers(ctx, repoPath, ident, normalizedFile))
	}
	var callerFlat []string
	for _, group := range callerGroups {
		callerFlat = append(callerFlat, group...)
	}
	callerSnippets := dedupeStrings(callerFlat)
	if len(callerSnippets) > 10 {
		callerSnippets = callerSnippets[:10]
	}

	var crossRefResults []string
	for _, path := range firstN(mentionedFiles, 10) {
		crossRefResults = append(crossRefResults, readCodeSnippet(repoPath, path, 1, 30))
	}
	var crossRefNonEmpty []string
	for _, item := range crossRefResults {
		if item != "" {
			crossRefNonEmpty = append(crossRefNonEmpty, item)
		}
	}
	crossRefSnippets := dedupeStrings(crossRefNonEmpty)

	return EvidencePackage{
		FindingTitle:     finding.Title,
		PrimaryCode:      primaryCode,
		CallerSnippets:   callerSnippets,
		CrossRefSnippets: crossRefSnippets,
		DiffHunk:         diffHunk,
		ImportContext:    importContext,
		RelatedCode:      relatedCode,
	}
}

// ReadCodeSnippet exposes _read_code_snippet (±context_lines around line).
func ReadCodeSnippet(repoPath, filePath string, line, contextLines int) string {
	return readCodeSnippet(repoPath, filePath, line, contextLines)
}

func readCodeSnippet(repoPath, filePath string, line, contextLines int) string {
	normalized := normalizeRelativePath(repoPath, filePath)
	absPath := filepath.Join(repoPath, normalized)
	if !isTextFile(absPath) {
		return ""
	}
	lines := readFileLines(absPath)
	if len(lines) == 0 {
		return ""
	}

	targetLine := line
	if targetLine < 1 {
		targetLine = 1
	}
	startIdx := targetLine - 1 - contextLines
	if startIdx < 0 {
		startIdx = 0
	}
	endIdx := targetLine + contextLines
	if endIdx > len(lines) {
		endIdx = len(lines)
	}

	var b strings.Builder
	for idx := startIdx; idx < endIdx; idx++ {
		if idx > startIdx {
			b.WriteByte('\n')
		}
		b.WriteString(fmt.Sprintf("%d: %s", idx+1, rstrip(lines[idx])))
	}
	return b.String()
}

// FindFunctionCallers exposes _find_function_callers.
func FindFunctionCallers(ctx context.Context, repoPath, functionName, excludeFile string) []string {
	return findFunctionCallers(ctx, repoPath, functionName, excludeFile)
}

func findFunctionCallers(ctx context.Context, repoPath, functionName, excludeFile string) []string {
	ident := strings.TrimSpace(functionName)
	if ident == "" || !identRe.MatchString(ident) {
		return []string{}
	}

	// ident is guaranteed [A-Za-z_][A-Za-z0-9_]*, so re.escape(ident) == ident.
	pattern := `\b` + ident + `\s*\(`
	args := append([]string{"-RInE", pattern, "."}, skipDirGrepArgs...)
	stdout, ran := runGrep(ctx, repoPath, args)
	if !ran {
		stdout = fallbackFunctionGrep(repoPath, pattern)
	}

	normalizedExclude := normalizeRelativePath(repoPath, excludeFile)
	var snippets []string

	for _, rawLine := range splitLines(stdout) {
		parts := strings.SplitN(rawLine, ":", 3)
		if len(parts) < 3 {
			continue
		}
		relPath := normalizeRelativePath(repoPath, parts[0])
		if relPath == normalizedExclude {
			continue
		}
		if !isTextFile(filepath.Join(repoPath, relPath)) {
			continue
		}
		lineNo, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		snippet := readCodeSnippet(repoPath, relPath, lineNo, 5)
		if snippet != "" {
			header := fmt.Sprintf("%s:%d", relPath, lineNo)
			snippets = append(snippets, header+"\n"+snippet)
		}
		if len(snippets) >= 10 {
			break
		}
	}

	return dedupeStrings(snippets)
}

// extractMentionedIdentifiers ports _extract_mentioned_identifiers.
func extractMentionedIdentifiers(text string) []string {
	var candidates []string
	for _, m := range backtickIdentRe.FindAllStringSubmatch(text, -1) {
		candidates = append(candidates, m[1])
	}
	for _, m := range capWordsRe.FindAllStringSubmatch(text, -1) {
		candidates = append(candidates, m[1])
	}
	for _, m := range snakeCallRe.FindAllStringSubmatch(text, -1) {
		candidates = append(candidates, m[1])
	}

	seen := map[string]struct{}{}
	var out []string
	for _, raw := range candidates {
		name := strings.Trim(raw, "\x60 ") // strip backticks and spaces
		if len(name) < 3 {
			continue
		}
		if _, ok := commonIdentifierWords[strings.ToLower(name)]; ok {
			continue
		}
		if !identRe.MatchString(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// extractMentionedFilePaths ports _extract_mentioned_file_paths.
func extractMentionedFilePaths(text, repoPath string) []string {
	var values []string
	for _, m := range backtickPathRe.FindAllStringSubmatch(text, -1) {
		values = append(values, m[1])
	}
	for _, m := range pathLikeRe.FindAllStringSubmatch(text, -1) {
		values = append(values, m[1])
	}

	candidates := map[string]struct{}{}
	for _, value := range values {
		if !strings.Contains(value, "/") {
			continue
		}
		if strings.Contains(value, " ") {
			continue
		}
		normalized := normalizeRelativePath(repoPath, value)
		absPath := filepath.Join(repoPath, normalized)
		if isFile(absPath) {
			candidates[normalized] = struct{}{}
		}
	}

	out := make([]string, 0, len(candidates))
	for k := range candidates {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ExtractDiffHunk exposes _extract_diff_hunk. A nil line reproduces Python's
// line=None (return the whole patch, capped at 200 lines).
func ExtractDiffHunk(diffPatches map[string]string, filePath string, line *int) string {
	return extractDiffHunk(diffPatches, filePath, line)
}

func extractDiffHunk(diffPatches map[string]string, filePath string, line *int) string {
	normalized := normalizePatchKey(filePath)
	patch := diffPatches[normalized]

	if patch == "" {
		// Python scans dict.items() in insertion order; Go maps have no order,
		// so scan sorted keys for determinism (see package divergence note).
		keys := make([]string, 0, len(diffPatches))
		for k := range diffPatches {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if normalizePatchKey(key) == normalized {
				patch = diffPatches[key]
				break
			}
		}
	}

	if patch == "" {
		return ""
	}

	patchLines := splitLines(patch)
	if line == nil {
		return strings.Join(firstN(patchLines, 200), "\n")
	}

	hunkLines := extractHunkForLine(patchLines, *line)
	if len(hunkLines) == 0 {
		return strings.Join(firstN(patchLines, 200), "\n")
	}
	return strings.Join(firstN(hunkLines, 200), "\n")
}

// BuildImportContext exposes _build_import_context.
func BuildImportContext(ctx context.Context, repoPath, filePath string) string {
	return buildImportContext(ctx, repoPath, filePath)
}

func buildImportContext(ctx context.Context, repoPath, filePath string) string {
	normalized := normalizeRelativePath(repoPath, filePath)
	absPath := filepath.Join(repoPath, normalized)

	var imports []string
	if isTextFile(absPath) {
		if data, err := os.ReadFile(absPath); err == nil {
			for _, rawLine := range splitLines(string(data)) {
				stripped := strings.TrimSpace(rawLine)
				if strings.HasPrefix(stripped, "import ") || strings.HasPrefix(stripped, "from ") {
					imports = append(imports, stripped)
				}
			}
		}
	}

	moduleName := pathToModule(normalized)
	var importedBy []string

	if moduleName != "" {
		regex := `^\s*(?:from\s+` + reEscape(moduleName) + `\b|import\s+` + reEscape(moduleName) + `\b)`
		args := append([]string{"-RIlE", regex, ".", "--include=*.py"}, skipDirGrepArgs...)
		stdout, ran := runGrep(ctx, repoPath, args)
		if !ran {
			stdout = fallbackImportGrep(repoPath, regex)
		}
		{
			for _, rawPath := range splitLines(stdout) {
				rel := normalizeRelativePath(repoPath, rawPath)
				if rel != normalized {
					importedBy = append(importedBy, rel)
				}
			}
		}
	}

	importsList := "none"
	if len(imports) > 0 {
		importsList = strings.Join(firstN(imports, 30), ", ")
	}

	importedByList := "none"
	if len(importedBy) > 0 {
		importedByList = strings.Join(firstN(sortedUnique(importedBy), 30), ", ")
	}

	return "IMPORTS: " + importsList + "\nIMPORTED BY: " + importedByList
}

// extractBlastRadiusCode ports _extract_blast_radius_code.
func extractBlastRadiusCode(repoPath, filePath string, identifiers, blastRadius []string) string {
	if len(identifiers) == 0 || len(blastRadius) == 0 {
		return ""
	}

	normalizedTarget := normalizeRelativePath(repoPath, filePath)

	identRegexps := make([]*regexp.Regexp, len(identifiers))
	for i, ident := range identifiers {
		identRegexps[i] = regexp.MustCompile(`\b` + reEscape(ident) + `\b`)
	}

	var snippets []string
	for _, candidate := range blastRadius {
		normalized := normalizeRelativePath(repoPath, candidate)
		if normalized == normalizedTarget {
			continue
		}
		absPath := filepath.Join(repoPath, normalized)
		if !isTextFile(absPath) {
			continue
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		lines := splitLinesKeepEnds(string(data))
		if len(lines) == 0 {
			continue
		}

		for _, re := range identRegexps {
			matchIdx := -1
			for i, value := range lines {
				if re.MatchString(value) {
					matchIdx = i
					break
				}
			}
			if matchIdx < 0 {
				continue
			}
			snippet := formatLinesWithNumbers(lines, matchIdx+1, 10)
			if snippet != "" {
				snippets = append(snippets, normalized+":"+strconv.Itoa(matchIdx+1)+"\n"+snippet)
			}
			break
		}

		if len(snippets) >= 5 {
			break
		}
	}

	return strings.Join(firstN(snippets, 5), "\n\n")
}

// buildDimensionPack tuning constants (Python default kwargs — no caller
// overrides them, see design §G / orch.py:707).
const (
	dimPackMaxFiles        = 6
	dimPackMaxLinesPerFile = 400
	dimPackMaxChars        = 16000
)

// BuildDimensionPack ports build_dimension_pack. diffPatches is accepted for
// call-site parity but is unused by the Python implementation too.
func BuildDimensionPack(ctx context.Context, repoPath string, targetFiles []string, diffPatches map[string]string) string {
	_ = diffPatches
	if repoPath == "" || len(targetFiles) == 0 {
		return ""
	}
	var parts []string
	for _, fp := range firstN(targetFiles, dimPackMaxFiles) {
		rel := strings.TrimLeft(strings.TrimSpace(fp), "/")
		absPath := filepath.Join(repoPath, rel)
		if !isFile(absPath) {
			alt := normalizeRelativePath(repoPath, fp)
			if alt != "" {
				rel, absPath = alt, filepath.Join(repoPath, alt)
			}
		}
		if !(isFile(absPath) && isTextFile(absPath)) {
			continue
		}
		rawLines := readFileLines(absPath)
		lines := make([]string, len(rawLines))
		for i, ln := range rawLines {
			lines[i] = strings.TrimRight(ln, "\n") // Python rstrip("\n") — keeps a trailing \r
		}
		if len(lines) == 0 {
			continue
		}
		shown := firstN(lines, dimPackMaxLinesPerFile)
		var body strings.Builder
		for i, ln := range shown {
			if i > 0 {
				body.WriteByte('\n')
			}
			body.WriteString(fmt.Sprintf("%d: %s", i+1, ln))
		}
		trunc := ""
		if len(lines) > dimPackMaxLinesPerFile {
			trunc = fmt.Sprintf(" (showing first %d of %d)", dimPackMaxLinesPerFile, len(lines))
		}
		imp := buildImportContext(ctx, repoPath, rel)
		block := "### " + rel + trunc + "\n```\n" + body.String() + "\n```"
		if imp != "" {
			block += "\n_import/usage context:_ " + truncateChars(imp, 1200)
		}
		parts = append(parts, block)
	}
	blob := strings.Join(parts, "\n\n")
	return truncateChars(blob, dimPackMaxChars)
}

// normalizeRelativePath ports _normalize_relative_path.
func normalizeRelativePath(repoPath, filePath string) string {
	path := strings.ReplaceAll(strings.TrimSpace(filePath), "\\", "/")
	if path == "" {
		return ""
	}

	if strings.HasPrefix(path, "/workspaces/") {
		path = strings.Replace(path, "/workspaces/", "", 1)
	}
	if strings.HasPrefix(path, "./") {
		path = path[2:]
	}

	repoAbs := ""
	if repoPath != "" {
		if abs, err := filepath.Abs(repoPath); err == nil {
			repoAbs = abs
		}
	}
	pathAbs := ""
	if filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			pathAbs = abs
		}
	}

	if repoAbs != "" && strings.HasPrefix(pathAbs, repoAbs) {
		if rel, err := filepath.Rel(repoAbs, pathAbs); err == nil {
			path = rel
		}
	} else if strings.HasPrefix(path, "/") {
		path = strings.TrimLeft(path, "/")
	}

	repoName := ""
	if repoAbs != "" {
		repoName = filepath.Base(repoAbs)
	}
	marker := repoName + "/"
	if marker != "" && strings.Contains(path, marker) {
		if idx := strings.Index(path, marker); idx >= 0 {
			path = path[idx+len(marker):]
		}
	}

	return filepath.ToSlash(filepath.Clean(path))
}

// normalizePatchKey ports _normalize_patch_key.
func normalizePatchKey(filePath string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(filePath, "\\", "/"))
	for _, prefix := range []string{"a/", "b/"} {
		if strings.HasPrefix(normalized, prefix) {
			normalized = normalized[len(prefix):]
		}
	}
	return strings.TrimLeft(normalized, "/")
}

// extractHunkForLine ports _extract_hunk_for_line.
func extractHunkForLine(patchLines []string, line int) []string {
	var currentHunk []string
	currentStart := 0
	currentCount := 0

	for _, raw := range patchLines {
		if strings.HasPrefix(raw, "@@") {
			if len(currentHunk) > 0 && currentCount > 0 && currentStart <= line && line < currentStart+currentCount {
				return currentHunk
			}
			currentHunk = []string{raw}
			if m := hunkHeaderRe.FindStringSubmatch(raw); m != nil {
				currentStart = mustAtoi(m[1])
				countStr := m[2]
				if countStr == "" {
					countStr = "1"
				}
				currentCount = mustAtoi(countStr)
			} else {
				currentStart = 0
				currentCount = 0
			}
		} else if len(currentHunk) > 0 {
			currentHunk = append(currentHunk, raw)
		}
	}

	if len(currentHunk) > 0 && currentCount > 0 && currentStart <= line && line < currentStart+currentCount {
		return currentHunk
	}
	return nil
}

// pathToModule ports _path_to_module.
func pathToModule(filePath string) string {
	if !strings.HasSuffix(filePath, ".py") {
		return ""
	}
	module := strings.ReplaceAll(filePath, "/", ".")
	switch {
	case strings.HasSuffix(module, ".__init__.py"):
		module = module[:len(module)-len(".__init__.py")]
	case strings.HasSuffix(module, ".py"):
		module = module[:len(module)-len(".py")]
	}
	return module
}

// formatLinesWithNumbers ports _format_lines_with_numbers.
func formatLinesWithNumbers(lines []string, targetLine, contextLines int) string {
	if len(lines) == 0 {
		return ""
	}
	startIdx := targetLine - 1 - contextLines
	if startIdx < 0 {
		startIdx = 0
	}
	endIdx := targetLine + contextLines
	if endIdx > len(lines) {
		endIdx = len(lines)
	}
	var b strings.Builder
	for idx := startIdx; idx < endIdx; idx++ {
		if idx > startIdx {
			b.WriteByte('\n')
		}
		b.WriteString(fmt.Sprintf("%d: %s", idx+1, rstrip(lines[idx])))
	}
	return b.String()
}

// dedupeStrings ports _dedupe_strings: order-preserving unique non-empty
// strings. Returns a non-nil slice.
func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// isTextFile ports _is_text_file.
func isTextFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	ext := strings.ToLower(splitExt(path))
	if _, ok := textExtensions[ext]; ok {
		return true
	}
	if ext != "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sample := make([]byte, 1024)
	n, _ := f.Read(sample)
	return !bytes.Contains(sample[:n], []byte{0})
}

// isFile mirrors os.path.isfile (follows symlinks, regular files only).
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// runGrep shells out to `grep` with cwd=repoPath and a 10s timeout, returning
// stdout and whether the process actually ran (ran=false reproduces Python
// catching OSError / TimeoutExpired -> []). Any exit code (0/1/2) counts as ran
// since Python uses check=False and only reads stdout.
func runGrep(ctx context.Context, repoPath string, args []string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "grep", args...)
	cmd.Dir = repoPath
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()

	if cctx.Err() != nil {
		return "", false // timeout or cancellation
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return "", false // could not start (e.g. grep not found) == OSError
	}
	return out.String(), true
}

// The production implementation follows Python by using grep when available.
// Windows development environments commonly lack it, so retain equivalent
// caller/import discovery with a small filesystem fallback.
func fallbackFunctionGrep(repoPath, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	var matches []string
	forEachRepoFile(repoPath, func(rel, abs string) {
		data, err := os.ReadFile(abs)
		if err != nil {
			return
		}
		for lineNo, line := range splitLines(string(data)) {
			if re.MatchString(line) {
				matches = append(matches, rel+":"+strconv.Itoa(lineNo+1)+":"+line)
			}
		}
	})
	return strings.Join(matches, "\n")
}

func fallbackImportGrep(repoPath, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	var matches []string
	forEachRepoFile(repoPath, func(rel, abs string) {
		if !strings.HasSuffix(rel, ".py") {
			return
		}
		data, err := os.ReadFile(abs)
		if err == nil && re.Match(data) {
			matches = append(matches, rel)
		}
	})
	return strings.Join(matches, "\n")
}

func forEachRepoFile(repoPath string, visit func(rel, abs string)) {
	_ = filepath.WalkDir(repoPath, func(abs string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			if abs != repoPath && isSkippedGrepDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(repoPath, abs)
		if err == nil {
			visit(filepath.ToSlash(rel), abs)
		}
		return nil
	})
}

func isSkippedGrepDir(name string) bool {
	for _, skipped := range []string{".git", "node_modules", "__pycache__", ".venv", "vendor", "venv"} {
		if name == skipped {
			return true
		}
	}
	return false
}

// readFileLines ports _read_file_lines (minus the perf cache): the file split
// into lines WITH their terminators, or nil on read error.
func readFileLines(absPath string) []string {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	return splitLinesKeepEnds(string(data))
}

// rstrip mirrors Python str.rstrip() (strip trailing Unicode whitespace).
func rstrip(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// splitExt ports os.path.splitext(path)[1]: the extension WITH its dot, or "".
// Unlike filepath.Ext, a leading-dot filename (".gitignore") has no extension.
func splitExt(path string) string {
	sepIdx := strings.LastIndexByte(path, '/')
	dotIdx := strings.LastIndexByte(path, '.')
	if dotIdx > sepIdx {
		filenameIdx := sepIdx + 1
		for filenameIdx < dotIdx {
			if path[filenameIdx] != '.' {
				return path[dotIdx:]
			}
			filenameIdx++
		}
	}
	return ""
}

// firstN returns s[:n] bounded by len(s), like Python list slicing s[:n].
func firstN[T any](s []T, n int) []T {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// truncateChars returns the first n runes of s, like Python str slicing s[:n].
func truncateChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// sortedUnique returns the sorted unique values (like sorted(set(...))).
func sortedUnique(values []string) []string {
	set := map[string]struct{}{}
	for _, v := range values {
		set[v] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// reEscape mirrors Python 3.7+ re.escape: escapes only the special-character
// set CPython uses (_special_chars_map). For validated identifiers/module names
// this only ever escapes '.' (and, defensively, '-').
func reEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && reSpecialChars[byte(r)] {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var reSpecialChars = func() map[byte]bool {
	m := map[byte]bool{}
	for _, c := range []byte("()[]{}?*+-|^$\\.&~# \t\n\r\v\f") {
		m[c] = true
	}
	return m
}()

// splitLinesKeepEnds ports str.splitlines(keepends=True): split on Python's
// universal newline set, keeping each terminator with its line, no trailing
// empty element.
func splitLinesKeepEnds(s string) []string {
	var lines []string
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		b.WriteRune(r)
		if r == '\r' {
			if i+1 < len(runes) && runes[i+1] == '\n' {
				b.WriteRune('\n')
				i++
			}
			lines = append(lines, b.String())
			b.Reset()
		} else if isLineBoundary(r) {
			lines = append(lines, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		lines = append(lines, b.String())
	}
	return lines
}

// splitLines ports str.splitlines() (keepends=False).
func splitLines(s string) []string {
	kept := splitLinesKeepEnds(s)
	out := make([]string, len(kept))
	for i, ln := range kept {
		out[i] = stripLineEnding(ln)
	}
	return out
}

func stripLineEnding(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return s[:len(s)-2]
	}
	if s == "" {
		return s
	}
	// splitLinesKeepEnds only ever appends a boundary rune (or \r\n) as the
	// terminator, so the last rune is the ending to strip when it is one
	// (handles multi-byte NEL/LS/PS correctly).
	r, size := utf8.DecodeLastRuneInString(s)
	if r == '\r' || isLineBoundary(r) {
		return s[:len(s)-size]
	}
	return s
}

func isLineBoundary(r rune) bool {
	switch r {
	case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}
