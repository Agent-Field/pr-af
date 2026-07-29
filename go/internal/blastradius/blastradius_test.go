package blastradius

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// writeFile writes content to repo/rel, creating parent dirs.
func writeFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	abs := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

// buildFixture constructs the reference repo used across tests. Its expected
// import graph and blast radii were captured from the Python implementation.
func buildFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "utils.py", "def util_func():\n    return 1\n")
	writeFile(t, repo, "app.py", "from utils import util_func\nimport helpers\n\ndef main():\n    return util_func()\n")
	writeFile(t, repo, "helpers.py", "def helper():\n    return 2\n")
	writeFile(t, repo, "relative.py", "from . import utils\nfrom .pkg import mod\nimport utils.deep.thing\n")
	writeFile(t, repo, "pkg/__init__.py", "X = 1\n")
	writeFile(t, repo, "uses_pkg.py", "from pkg import mod\nimport pkg.mod\n")
	writeFile(t, repo, "pkg/mod.py", "def mod_fn():\n    return 3\n")
	// A file inside venv/ imports utils but MUST be skipped.
	writeFile(t, repo, "venv/lib/skipme.py", "import utils\n")
	return repo
}

func TestBuildImportGraph(t *testing.T) {
	repo := buildFixture(t)
	got := BuildImportGraph(repo)

	// Golden captured from Python build_import_graph.
	want := map[string][]string{
		"app.py":      {"utils.py", "helpers.py"},
		"uses_pkg.py": {"pkg/__init__.py", "pkg/mod.py"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("import graph mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestRelativeImportsProduceNoEdges(t *testing.T) {
	repo := buildFixture(t)
	got := BuildImportGraph(repo)
	// relative.py uses `from . import utils`, `from .pkg import mod`, and
	// `import utils.deep.thing` — all deliberately unresolvable, so it must not
	// be a key in the graph at all.
	if edges, present := got["relative.py"]; present {
		t.Fatalf("relative.py must have no edges, got %v", edges)
	}
}

func TestVendorAndVenvDirsSkipped(t *testing.T) {
	repo := buildFixture(t)
	got := BuildImportGraph(repo)
	// venv/lib/skipme.py imports utils but the venv/ subtree is skipped, so it
	// must never appear as a dependent.
	if _, present := got["venv/lib/skipme.py"]; present {
		t.Fatal("venv/lib/skipme.py should be skipped")
	}
	// And it must not surface in the blast radius of a changed utils.py.
	for _, f := range ComputeBlastRadius([]string{"utils.py"}, repo) {
		if f == "venv/lib/skipme.py" {
			t.Fatal("skipped file leaked into blast radius")
		}
	}
}

func TestFromImportResolvesModuleNotName(t *testing.T) {
	repo := buildFixture(t)
	got := BuildImportGraph(repo)
	// `from pkg import mod` resolves the package module `pkg`
	// (pkg/__init__.py), NOT pkg/mod.py; `import pkg.mod` resolves pkg/mod.py.
	edges := got["uses_pkg.py"]
	want := []string{"pkg/__init__.py", "pkg/mod.py"}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("uses_pkg.py edges = %v, want %v", edges, want)
	}
}

func TestComputeBlastRadius(t *testing.T) {
	repo := buildFixture(t)
	cases := []struct {
		name    string
		changed []string
		want    []string
	}{
		{"utils changed -> app affected", []string{"utils.py"}, []string{"app.py"}},
		{"pkg/mod changed -> uses_pkg affected", []string{"pkg/mod.py"}, []string{"uses_pkg.py"}},
		{"helpers changed -> app affected", []string{"helpers.py"}, []string{"app.py"}},
		{"pkg/__init__ changed -> uses_pkg affected", []string{"pkg/__init__.py"}, []string{"uses_pkg.py"}},
		{"no importers -> empty", []string{"relative.py"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeBlastRadius(tc.changed, repo)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ComputeBlastRadius(%v) = %v, want %v", tc.changed, got, tc.want)
			}
		})
	}
}

func TestComputeBlastRadiusExcludesChangedFiles(t *testing.T) {
	repo := buildFixture(t)
	// If both utils.py AND app.py changed, app.py (a dependent) must be
	// excluded because it is itself in the changeset.
	got := ComputeBlastRadius([]string{"utils.py", "app.py"}, repo)
	if len(got) != 0 {
		t.Fatalf("expected empty (dependent is itself changed), got %v", got)
	}
}

func TestComputeBlastRadiusInvalidRepo(t *testing.T) {
	if got := ComputeBlastRadius([]string{"x.py"}, ""); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("empty repo path -> %v, want []", got)
	}
	if got := ComputeBlastRadius([]string{"x.py"}, filepath.Join(t.TempDir(), "does-not-exist")); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("missing repo -> %v, want []", got)
	}
	// A file (not dir) is also rejected.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ComputeBlastRadius([]string{"x.py"}, f); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("repo is a file -> %v, want []", got)
	}
}

func TestExtractPythonImports(t *testing.T) {
	src := "import os\nfrom sys import argv\n  import indented\nimport a.b.c\n" +
		"from . import rel\nfrom .pkg import x\nimport os, sys\n# import commented\n"
	got := extractPythonImports(src)
	// Captures: `import\s+([\w.]+)` / `from\s+([\w.]+)` — first token only, and
	// the leading-# comment line still matches at `^\s*import` only if it begins
	// with import/from (it does not: it begins with '#').
	want := []string{"os", "sys", "indented", "a.b.c", ".", ".pkg", "os"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractPythonImports = %v, want %v", got, want)
	}
}

func TestBuildPythonModuleMap(t *testing.T) {
	repo := t.TempDir()
	files := []string{
		filepath.Join(repo, "a", "b.py"),
		filepath.Join(repo, "pkg", "__init__.py"),
		filepath.Join(repo, "top.py"),
	}
	got := buildPythonModuleMap(files, repo)
	want := map[string]string{
		"a.b": "a/b.py",
		"pkg": "pkg/__init__.py",
		"top": "top.py",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("module map = %v, want %v", got, want)
	}
}

func TestContainsSkipToken(t *testing.T) {
	// Substring semantics, matching Python `skip in rel_root`.
	trueCases := []string{".git", "a/.git/b", "node_modules", "src/venv", "myvenv", ".github", "x/.venv/y", "__pycache__"}
	for _, c := range trueCases {
		if !containsSkipToken(c) {
			t.Errorf("containsSkipToken(%q) = false, want true", c)
		}
	}
	falseCases := []string{".", "src", "app", "pkg/mod"}
	for _, c := range falseCases {
		if containsSkipToken(c) {
			t.Errorf("containsSkipToken(%q) = true, want false", c)
		}
	}
}

// TestBlastRadiusOutputSorted guards the sorted, non-nil return contract.
func TestBlastRadiusOutputSorted(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "core.py", "X=1\n")
	writeFile(t, repo, "z.py", "import core\n")
	writeFile(t, repo, "a.py", "import core\n")
	writeFile(t, repo, "m.py", "import core\n")
	got := ComputeBlastRadius([]string{"core.py"}, repo)
	want := []string{"a.py", "m.py", "z.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blast radius = %v, want sorted %v", got, want)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("output not sorted")
	}
}
