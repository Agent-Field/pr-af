package diffengine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Expected values in this file are ground truth captured from the Python source
// of truth (src/pr_af/diff_engine.py) run against each fixture — see
// scratchpad/gen_truth.py. Tests are derived from observable behavior (exact
// FileChange/Hunk fields, DiffStats numbers, cluster membership/ids/order), not
// from the Go implementation.

// hunk is a terse Hunk constructor for expected values.
func hunk(oldStart, oldCount, newStart, newCount int, header, content string) schemas.Hunk {
	return schemas.Hunk{
		OldStart: oldStart, OldCount: oldCount,
		NewStart: newStart, NewCount: newCount,
		Header: header, Content: content,
	}
}

// jsonEq marshals two values to JSON and reports whether they are identical,
// returning both renderings for a readable failure message. Both arguments are
// the same concrete Go type, so JSON key ordering is identical and any
// difference is a genuine value difference.
func jsonEq(t *testing.T, got, want any) (string, string, bool) {
	t.Helper()
	g, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	w, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	return string(g), string(w), reflect.DeepEqual(got, want)
}

type diffCase struct {
	name         string
	diff         string
	wantFiles    []schemas.FileChange
	wantStats    schemas.DiffStats
	wantClusters []schemas.ChangeCluster
}

func diffCases() []diffCase {
	return []diffCase{
		{
			name:      "empty",
			diff:      "",
			wantFiles: []schemas.FileChange{},
			wantStats: schemas.DiffStats{
				TotalFiles: 0, TotalAdditions: 0, TotalDeletions: 0,
				FilesAdded: 0, FilesModified: 0, FilesRemoved: 0, FilesRenamed: 0,
				TestFilesChanged: 0, TestToCodeRatio: 0.0,
			},
			wantClusters: []schemas.ChangeCluster{},
		},
		{
			name: "single_modified",
			diff: "diff --git a/src/app.py b/src/app.py\n" +
				"index 111..222 100644\n" +
				"--- a/src/app.py\n" +
				"+++ b/src/app.py\n" +
				"@@ -10,3 +10,4 @@ def handler():\n" +
				" context line\n" +
				"-old line\n" +
				"+new line one\n" +
				"+new line two\n",
			wantFiles: []schemas.FileChange{{
				Path: "src/app.py", Status: "modified", Language: "python",
				LinesAdded: 2, LinesRemoved: 1,
				Hunks: []schemas.Hunk{hunk(10, 3, 10, 4, "@@ -10,3 +10,4 @@ def handler():",
					" context line\n-old line\n+new line one\n+new line two")},
			}},
			wantStats: schemas.DiffStats{
				TotalFiles: 1, TotalAdditions: 2, TotalDeletions: 1,
				FilesModified: 1, TestToCodeRatio: 0.0,
			},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "src", Files: []string{"src/app.py"}, PrimaryLanguage: "python"},
			},
		},
		{
			name: "omitted_counts",
			diff: "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -5 +6 @@\n-a\n+b\n",
			wantFiles: []schemas.FileChange{{
				Path: "x.go", Status: "modified", Language: "go",
				LinesAdded: 1, LinesRemoved: 1,
				Hunks: []schemas.Hunk{hunk(5, 1, 6, 1, "@@ -5 +6 @@", "-a\n+b")},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, TotalAdditions: 1, TotalDeletions: 1, FilesModified: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{"x.go"}, PrimaryLanguage: "go"},
			},
		},
		{
			name: "new_file",
			diff: "diff --git a/newmod.ts b/newmod.ts\n" +
				"new file mode 100644\nindex 000..abc\n" +
				"--- /dev/null\n+++ b/newmod.ts\n" +
				"@@ -0,0 +1,2 @@\n+export const x = 1;\n+export const y = 2;\n",
			wantFiles: []schemas.FileChange{{
				Path: "newmod.ts", Status: "added", Language: "typescript",
				LinesAdded: 2, LinesRemoved: 0,
				Hunks: []schemas.Hunk{hunk(0, 0, 1, 2, "@@ -0,0 +1,2 @@",
					"+export const x = 1;\n+export const y = 2;")},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, TotalAdditions: 2, FilesAdded: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{"newmod.ts"}, PrimaryLanguage: "typescript"},
			},
		},
		{
			name: "deleted_file",
			diff: "diff --git a/old.rb b/old.rb\ndeleted file mode 100644\nindex abc..000\n" +
				"--- a/old.rb\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-line1\n-line2\n-line3\n",
			// Deleted files never set a path (no +++ b/ line): Path is "".
			wantFiles: []schemas.FileChange{{
				Path: "", Status: "removed", Language: "",
				LinesAdded: 0, LinesRemoved: 3,
				Hunks: []schemas.Hunk{hunk(1, 3, 0, 0, "@@ -1,3 +0,0 @@", "-line1\n-line2\n-line3")},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, TotalDeletions: 3, FilesRemoved: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{""}, PrimaryLanguage: ""},
			},
		},
		{
			name: "rename_content",
			diff: "diff --git a/old_name.py b/new_name.py\nsimilarity index 90%\n" +
				"rename from old_name.py\nrename to new_name.py\n" +
				"--- a/old_name.py\n+++ b/new_name.py\n@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n",
			// Renames are NOT special-cased: status stays "modified", path from +++ b/.
			wantFiles: []schemas.FileChange{{
				Path: "new_name.py", Status: "modified", Language: "python",
				LinesAdded: 1, LinesRemoved: 1,
				Hunks: []schemas.Hunk{hunk(1, 3, 1, 3, "@@ -1,3 +1,3 @@", " ctx\n-old\n+new")},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, TotalAdditions: 1, TotalDeletions: 1, FilesModified: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{"new_name.py"}, PrimaryLanguage: "python"},
			},
		},
		{
			name: "pure_rename",
			diff: "diff --git a/a.py b/b.py\nsimilarity index 100%\nrename from a.py\nrename to b.py\n",
			// No +++ b/ and no hunks: Path "", status "modified", empty hunks.
			wantFiles: []schemas.FileChange{{
				Path: "", Status: "modified", Language: "", Hunks: []schemas.Hunk{},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, FilesModified: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{""}, PrimaryLanguage: ""},
			},
		},
		{
			name: "binary",
			diff: "diff --git a/img.png b/img.png\nindex abc..def 100644\n" +
				"Binary files a/img.png and b/img.png differ\n",
			wantFiles: []schemas.FileChange{{
				Path: "", Status: "modified", Language: "", Hunks: []schemas.Hunk{},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, FilesModified: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{""}, PrimaryLanguage: ""},
			},
		},
		{
			name: "multi_file",
			diff: "diff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n" +
				"@@ -1,2 +1,3 @@\n x\n+added1\n" +
				"@@ -10,2 +11,2 @@\n-removed10\n+added10\n" +
				"diff --git a/pkg/b_test.go b/pkg/b_test.go\n--- a/pkg/b_test.go\n+++ b/pkg/b_test.go\n" +
				"@@ -1,1 +1,2 @@\n+added_b\n",
			wantFiles: []schemas.FileChange{
				{
					Path: "pkg/a.go", Status: "modified", Language: "go",
					LinesAdded: 2, LinesRemoved: 1,
					Hunks: []schemas.Hunk{
						hunk(1, 2, 1, 3, "@@ -1,2 +1,3 @@", " x\n+added1"),
						hunk(10, 2, 11, 2, "@@ -10,2 +11,2 @@", "-removed10\n+added10"),
					},
				},
				{
					Path: "pkg/b_test.go", Status: "modified", Language: "go",
					LinesAdded: 1, LinesRemoved: 0,
					Hunks: []schemas.Hunk{hunk(1, 1, 1, 2, "@@ -1,1 +1,2 @@", "+added_b")},
				},
			},
			wantStats: schemas.DiffStats{
				TotalFiles: 2, TotalAdditions: 3, TotalDeletions: 1,
				FilesModified: 2, TestFilesChanged: 1, TestToCodeRatio: 1.0,
			},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "pkg", Files: []string{"pkg/a.go", "pkg/b_test.go"}, PrimaryLanguage: "go"},
			},
		},
		{
			name: "content_quirks",
			diff: "diff --git a/q.py b/q.py\n--- a/q.py\n+++ b/q.py\n@@ -1,4 +1,4 @@\n" +
				"+++normal_add\n---normal_del\n+real_add\n-real_del\n" +
				"--- a/swallowed\n\\ No newline at end of file\n",
			// "+++normal_add" / "---normal_del" are NOT counted; "--- a/swallowed"
			// is swallowed by the header branch (absent from content and uncounted);
			// the "\ No newline" marker is appended to content but uncounted.
			wantFiles: []schemas.FileChange{{
				Path: "q.py", Status: "modified", Language: "python",
				LinesAdded: 1, LinesRemoved: 1,
				Hunks: []schemas.Hunk{hunk(1, 4, 1, 4, "@@ -1,4 +1,4 @@",
					"+++normal_add\n---normal_del\n+real_add\n-real_del\n\\ No newline at end of file")},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, TotalAdditions: 1, TotalDeletions: 1, FilesModified: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{"q.py"}, PrimaryLanguage: "python"},
			},
		},
		{
			name: "malformed_hunk",
			diff: "diff --git a/m.py b/m.py\n--- a/m.py\n+++ b/m.py\n@@ -1,1 +1,1 @@\n" +
				"+first\n@@ malformed hunk header\n+second\n",
			// The malformed "@@" line does not match the hunk regex, so current_hunk
			// is left unchanged and "+second" folds into the first hunk.
			wantFiles: []schemas.FileChange{{
				Path: "m.py", Status: "modified", Language: "python",
				LinesAdded: 2, LinesRemoved: 0,
				Hunks: []schemas.Hunk{hunk(1, 1, 1, 1, "@@ -1,1 +1,1 @@", "+first\n+second")},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, TotalAdditions: 2, FilesModified: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{"m.py"}, PrimaryLanguage: "python"},
			},
		},
		{
			name:         "malformed_no_header",
			diff:         "this is not a diff\njust some text\n@@ -1 +1 @@\n+foo\n",
			wantFiles:    []schemas.FileChange{},
			wantStats:    schemas.DiffStats{},
			wantClusters: []schemas.ChangeCluster{},
		},
		{
			name: "header_only",
			diff: "diff --git a/foo b/foo\n",
			wantFiles: []schemas.FileChange{{
				Path: "", Status: "modified", Language: "", Hunks: []schemas.Hunk{},
			}},
			wantStats: schemas.DiffStats{TotalFiles: 1, FilesModified: 1},
			wantClusters: []schemas.ChangeCluster{
				{ID: "cluster_0", Name: "root", Files: []string{""}, PrimaryLanguage: ""},
			},
		},
		{
			name: "cluster_mix",
			diff: "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n+r\n" +
				"diff --git a/src/a.py b/src/a.py\n--- a/src/a.py\n+++ b/src/a.py\n@@ -1 +1 @@\n+a\n" +
				"diff --git a/src/b.py b/src/b.py\n--- a/src/b.py\n+++ b/src/b.py\n@@ -1 +1 @@\n+b\n" +
				"diff --git a/src/c.js b/src/c.js\n--- a/src/c.js\n+++ b/src/c.js\n@@ -1 +1 @@\n+c\n",
			wantFiles: []schemas.FileChange{
				{Path: "README.md", Status: "modified", Language: "markdown", LinesAdded: 1,
					Hunks: []schemas.Hunk{hunk(1, 1, 1, 1, "@@ -1 +1 @@", "+r")}},
				{Path: "src/a.py", Status: "modified", Language: "python", LinesAdded: 1,
					Hunks: []schemas.Hunk{hunk(1, 1, 1, 1, "@@ -1 +1 @@", "+a")}},
				{Path: "src/b.py", Status: "modified", Language: "python", LinesAdded: 1,
					Hunks: []schemas.Hunk{hunk(1, 1, 1, 1, "@@ -1 +1 @@", "+b")}},
				{Path: "src/c.js", Status: "modified", Language: "javascript", LinesAdded: 1,
					Hunks: []schemas.Hunk{hunk(1, 1, 1, 1, "@@ -1 +1 @@", "+c")}},
			},
			wantStats: schemas.DiffStats{TotalFiles: 4, TotalAdditions: 4, FilesModified: 4},
			wantClusters: []schemas.ChangeCluster{
				// Sorted by directory: "root" < "src".
				{ID: "cluster_0", Name: "root", Files: []string{"README.md"}, PrimaryLanguage: "markdown"},
				{ID: "cluster_1", Name: "src", Files: []string{"src/a.py", "src/b.py", "src/c.js"}, PrimaryLanguage: "python"},
			},
		},
	}
}

func TestParseUnifiedDiff(t *testing.T) {
	for _, tc := range diffCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseUnifiedDiff(tc.diff)
			if g, w, ok := jsonEq(t, got, tc.wantFiles); !ok {
				t.Errorf("ParseUnifiedDiff files mismatch\n got: %s\nwant: %s", g, w)
			}
		})
	}
}

func TestComputeDiffStats(t *testing.T) {
	for _, tc := range diffCases() {
		t.Run(tc.name, func(t *testing.T) {
			files := ParseUnifiedDiff(tc.diff)
			got := ComputeDiffStats(files)
			if g, w, ok := jsonEq(t, got, tc.wantStats); !ok {
				t.Errorf("ComputeDiffStats mismatch\n got: %s\nwant: %s", g, w)
			}
		})
	}
}

func TestClusterChanges(t *testing.T) {
	for _, tc := range diffCases() {
		t.Run(tc.name, func(t *testing.T) {
			files := ParseUnifiedDiff(tc.diff)
			got := ClusterChanges(files)
			if g, w, ok := jsonEq(t, got, tc.wantClusters); !ok {
				t.Errorf("ClusterChanges mismatch\n got: %s\nwant: %s", g, w)
			}
		})
	}
}

// TestParseUnifiedDiffCRLF locks in the str.splitlines() parity: a diff with
// CRLF (\r\n) line endings must parse identically to the LF version — the \r is
// consumed as part of the boundary and never leaks into headers or content.
func TestParseUnifiedDiffCRLF(t *testing.T) {
	lf := "diff --git a/src/app.py b/src/app.py\n--- a/src/app.py\n+++ b/src/app.py\n" +
		"@@ -1,2 +1,2 @@\n context\n-old\n+new\n"
	crlf := ""
	for _, r := range lf {
		if r == '\n' {
			crlf += "\r\n"
		} else {
			crlf += string(r)
		}
	}
	gotLF := ParseUnifiedDiff(lf)
	gotCRLF := ParseUnifiedDiff(crlf)
	if !reflect.DeepEqual(gotLF, gotCRLF) {
		lfJSON, _ := json.MarshalIndent(gotLF, "", "  ")
		crlfJSON, _ := json.MarshalIndent(gotCRLF, "", "  ")
		t.Errorf("CRLF parse differs from LF\n LF: %s\nCRLF: %s", lfJSON, crlfJSON)
	}
	if len(gotCRLF) != 1 || gotCRLF[0].Hunks[0].Content != " context\n-old\n+new" {
		t.Errorf("CRLF content = %q, want %q", gotCRLF[0].Hunks[0].Content, " context\n-old\n+new")
	}
}

// TestParseUnifiedDiffTrailingNewline verifies that a trailing "\n" does not add
// a spurious empty final line to the last hunk's content (splitlines behavior).
func TestParseUnifiedDiffTrailingNewline(t *testing.T) {
	withNL := ParseUnifiedDiff("diff --git a/t.py b/t.py\n--- a/t.py\n+++ b/t.py\n@@ -1 +1 @@\n-a\n+b\n")
	withoutNL := ParseUnifiedDiff("diff --git a/t.py b/t.py\n--- a/t.py\n+++ b/t.py\n@@ -1 +1 @@\n-a\n+b")
	if !reflect.DeepEqual(withNL, withoutNL) {
		t.Fatalf("trailing newline changed result: %+v vs %+v", withNL, withoutNL)
	}
	if got := withNL[0].Hunks[0].Content; got != "-a\n+b" {
		t.Errorf("content = %q, want %q (no trailing empty line)", got, "-a\n+b")
	}
}

func TestDetectLanguage(t *testing.T) {
	// Ground truth from Python _detect_language.
	want := map[string]string{
		"a.py": "python", "a.js": "javascript", "a.ts": "typescript",
		"a.tsx": "typescript", "a.jsx": "javascript", "a.go": "go",
		"a.rs": "rust", "a.java": "java", "a.rb": "ruby", "a.cpp": "cpp",
		"a.c": "c", "a.cs": "csharp", "a.swift": "swift", "a.kt": "kotlin",
		"a.scala": "scala", "a.php": "php", "a.sh": "bash", "a.yaml": "yaml",
		"a.yml": "yaml", "a.json": "json", "a.md": "markdown", "a.sql": "sql",
		"a.html": "html", "a.css": "css", "a.unknown": "", "noext": "",
	}
	for path, w := range want {
		if got := detectLanguage(path); got != w {
			t.Errorf("detectLanguage(%q) = %q, want %q", path, got, w)
		}
	}
}

func TestIsTestFile(t *testing.T) {
	// Ground truth from Python _is_test_file. Note "latest/foo.py" is true — the
	// patterns are substrings, and "latest/" contains "test/". Case-insensitive.
	want := map[string]bool{
		"src/test_foo.py": true, "foo_test.go": true, "foo.test.js": true,
		"tests/x.py": true, "test/x.py": true, "__tests__/x.js": true,
		"spec/x.rb": true, "latest/foo.py": true, "src/app.py": false,
		"TEST_FOO.PY": true, "Tests/X.py": true, "": false,
	}
	for path, w := range want {
		if got := isTestFile(path); got != w {
			t.Errorf("isTestFile(%q) = %v, want %v", path, got, w)
		}
	}
}

// TestPrimaryLanguageTieBreak documents the one deliberate divergence from
// Python: on a language-count tie, Python's max(set(langs), key=langs.count)
// tie-break is set-iteration-order dependent (non-deterministic across Python
// processes due to str hash randomization). This port breaks ties by
// first-appearance order within the group, which is deterministic. A unique
// mode (the common case) agrees with Python regardless.
func TestPrimaryLanguageTieBreak(t *testing.T) {
	goFirst := []schemas.FileChange{
		{Path: "d/x.go", Language: "go"},
		{Path: "d/y.py", Language: "python"},
	}
	if got := primaryLanguage(goFirst); got != "go" {
		t.Errorf("tie primaryLanguage(go,python) = %q, want go (first-in-list)", got)
	}
	pyFirst := []schemas.FileChange{
		{Path: "d/y.py", Language: "python"},
		{Path: "d/x.go", Language: "go"},
	}
	if got := primaryLanguage(pyFirst); got != "python" {
		t.Errorf("tie primaryLanguage(python,go) = %q, want python (first-in-list)", got)
	}
	// Unique mode: python appears twice, go once.
	mode := []schemas.FileChange{
		{Path: "d/a.go", Language: "go"},
		{Path: "d/b.py", Language: "python"},
		{Path: "d/c.py", Language: "python"},
	}
	if got := primaryLanguage(mode); got != "python" {
		t.Errorf("mode primaryLanguage = %q, want python", got)
	}
	// No languages -> "".
	if got := primaryLanguage([]schemas.FileChange{{Path: "d/x.unknown"}}); got != "" {
		t.Errorf("empty-lang primaryLanguage = %q, want \"\"", got)
	}
}
