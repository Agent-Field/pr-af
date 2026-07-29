// Package blastradius ports pr_af/blast_radius.py: a file-level dependency
// graph built from Python import statements, used to find files that are
// affected by a change but not part of the changeset.
//
// Parity note (design §A, task T2.3): the Python engine is DELIBERATELY
// language-limited to Python imports and this port reproduces that limitation
// verbatim — it does NOT understand Go, JS, or any other language's imports.
// Relative imports (`from . import x`, `from .foo import y`) capture a
// leading-dot module name that never matches a module-map key, so they never
// produce an edge — exactly as in Python.
package blastradius

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// skipDirs mirrors blast_radius.py's os.walk skip tuple. The Python code tests
// `skip in rel_root` — a SUBSTRING match on the directory's relative path — so
// a directory whose relative path merely CONTAINS one of these tokens is
// skipped (e.g. ".github" contains ".git"; "srcvenv" contains "venv"). This
// loose behavior is reproduced exactly.
var skipDirs = []string{".git", "node_modules", "vendor", "__pycache__", ".venv", "venv"}

// importRe ports _extract_python_imports's regex. `(?m)` == re.MULTILINE so `^`
// matches at each line start. `[\w.]+` captures the module path; in Go `\w` is
// ASCII-only, which is sufficient for Python module names (always ASCII).
var importRe = regexp.MustCompile(`(?m)^\s*(?:from|import)\s+([\w.]+)`)

// ComputeBlastRadius ports compute_blast_radius: files affected by changes but
// not themselves in the changeset. If file A imports file B and B changed (and
// A did not), A is in the blast radius. Returns a sorted, non-nil slice.
func ComputeBlastRadius(changedFiles []string, repoPath string) []string {
	if repoPath == "" {
		return []string{}
	}
	if info, err := os.Stat(repoPath); err != nil || !info.IsDir() {
		return []string{}
	}

	depGraph := BuildImportGraph(repoPath)

	changedSet := make(map[string]struct{}, len(changedFiles))
	for _, f := range changedFiles {
		changedSet[f] = struct{}{}
	}

	affected := map[string]struct{}{}
	for _, changed := range changedFiles {
		for dependent, imports := range depGraph {
			if _, isChanged := changedSet[dependent]; isChanged {
				continue
			}
			for _, imp := range imports {
				if imp == changed {
					affected[dependent] = struct{}{}
					break
				}
			}
		}
	}

	out := make([]string, 0, len(affected))
	for k := range affected {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BuildImportGraph ports build_import_graph: {file_path: [files_it_imports]}.
// Only Python (.py) files are considered, and only imports that resolve to a
// file within the repo produce an edge. Files with no resolving import do not
// appear as keys (matching Python's defaultdict -> dict(graph)).
func BuildImportGraph(repoPath string) map[string][]string {
	graph := map[string][]string{}

	var pyFiles []string
	// os.walk in Python descends the tree top-down and, for each directory,
	// skips (does not collect) files when the directory's relative path
	// contains a skip token. Because a skip token in an ancestor's rel path is
	// still a substring of every descendant's rel path, every file under a
	// skipped directory is also skipped — so pruning the subtree here is
	// equivalent to Python's per-directory `continue`, and cheaper.
	_ = filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// os.walk's default onerror swallows errors and keeps going.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == repoPath {
				return nil
			}
			relRoot, rerr := filepath.Rel(repoPath, path)
			if rerr == nil && containsSkipToken(relRoot) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".py") {
			pyFiles = append(pyFiles, path)
		}
		return nil
	})

	moduleToFile := buildPythonModuleMap(pyFiles, repoPath)

	for _, pyFile := range pyFiles {
		relPath := relPathOf(repoPath, pyFile)
		data, err := os.ReadFile(pyFile)
		if err != nil {
			continue
		}
		for _, mod := range extractPythonImports(string(data)) {
			if target, ok := moduleToFile[mod]; ok {
				graph[relPath] = append(graph[relPath], target)
			}
		}
	}

	return graph
}

// buildPythonModuleMap ports _build_python_module_map: module name -> relative
// file path. On a module-name collision the last file (in py_files order) wins,
// matching Python's plain-dict overwrite.
func buildPythonModuleMap(pyFiles []string, repoPath string) map[string]string {
	mapping := make(map[string]string, len(pyFiles))
	for _, pyFile := range pyFiles {
		rel := relPathOf(repoPath, pyFile)
		// Repository-relative paths are part of the cross-platform data contract;
		// normalize before turning a path into a Python module name.
		module := strings.ReplaceAll(filepath.ToSlash(rel), "/", ".")
		module = strings.TrimSuffix(module, ".py")
		module = strings.TrimSuffix(module, ".__init__")
		mapping[module] = rel
	}
	return mapping
}

// extractPythonImports ports _extract_python_imports.
func extractPythonImports(content string) []string {
	matches := importRe.FindAllStringSubmatch(content, -1)
	modules := make([]string, 0, len(matches))
	for _, m := range matches {
		modules = append(modules, m[1])
	}
	return modules
}

// relPathOf mirrors os.path.relpath(file, repoPath) using repository-standard
// slash separators, independent of the host OS.
func relPathOf(repoPath, file string) string {
	rel, err := filepath.Rel(repoPath, file)
	if err != nil {
		return file
	}
	return filepath.ToSlash(rel)
}

// containsSkipToken reports whether any skip token is a substring of relRoot,
// reproducing Python's `any(skip in rel_root for skip in ...)`.
func containsSkipToken(relRoot string) bool {
	for _, skip := range skipDirs {
		if strings.Contains(relRoot, skip) {
			return true
		}
	}
	return false
}
