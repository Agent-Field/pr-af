package evidence

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func writeFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	abs := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

// --- pure-function tests (grep-independent, fully portable) ---

func TestNormalizeRelativePath(t *testing.T) {
	repo := t.TempDir()
	cases := []struct {
		name, repo, in, want string
	}{
		{"dot-slash", repo, "./a/b.py", "a/b.py"},
		{"workspaces-prefix", repo, "/workspaces/x/y.py", "x/y.py"},
		{"abs-in-repo", repo, repo + "/utils.py", "utils.py"},
		{"repo-name-marker", "/home/x/keycloak", "org/keycloak/src/Foo.java", "src/Foo.java"},
		{"backslashes", repo, `a\b\c.py`, "a/b/c.py"},
		{"blank", repo, "   ", ""},
		{"abs-outside-repo", repo, "/etc/passwd", "etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRelativePath(tc.repo, tc.in); got != tc.want {
				t.Fatalf("normalizeRelativePath(%q,%q) = %q, want %q", tc.repo, tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePatchKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a/src/foo.py", "src/foo.py"},
		{"b/src/foo.py", "src/foo.py"},
		{"a/b/foo.py", "foo.py"},
		{"src/foo.py", "src/foo.py"},
		{`b\src\foo.py`, "src/foo.py"},
		{"/leading/slash.py", "leading/slash.py"},
	}
	for _, tc := range cases {
		if got := normalizePatchKey(tc.in); got != tc.want {
			t.Errorf("normalizePatchKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPathToModule(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pkg/utils.py", "pkg.utils"},
		{"pkg/__init__.py", "pkg"},
		{"pkg/foo.txt", ""},
		{"top.py", "top"},
	}
	for _, tc := range cases {
		if got := pathToModule(tc.in); got != tc.want {
			t.Errorf("pathToModule(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractMentionedIdentifiers(t *testing.T) {
	got := extractMentionedIdentifiers("The `util_func` calls MyClass and helper_fn() but not the value")
	want := []string{"util_func", "MyClass", "helper_fn"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identifiers = %v, want %v", got, want)
	}

	// Ordering across the three regex passes and common-word filtering.
	got2 := extractMentionedIdentifiers("`alpha` Beta gamma_call() the value error `alpha`")
	// backtick: alpha, alpha ; capwords: Beta ; snake-call: gamma_call.
	// "value","error","the" are common words. Dedup preserves first-seen order.
	want2 := []string{"alpha", "Beta", "gamma_call"}
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("identifiers2 = %v, want %v", got2, want2)
	}
}

func TestExtractMentionedFilePaths(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "pkg/mod.py", "x=1\n")
	writeFile(t, repo, "utils.py", "y=2\n")
	// Only slash-containing, space-free, existing files are kept. utils.py and
	// app.py have no slash -> filtered; only `pkg/mod.py` survives.
	got := extractMentionedFilePaths("see `pkg/mod.py` and utils.py and app.py referenced", repo)
	want := []string{"pkg/mod.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mentioned files = %v, want %v", got, want)
	}
}

func TestReadCodeSnippet(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.py", "from utils import util_func\nimport helpers\n\ndef main():\n    return util_func()\n")
	got := readCodeSnippet(repo, "app.py", 4, 2)
	want := "2: import helpers\n3: \n4: def main():\n5:     return util_func()"
	if got != want {
		t.Fatalf("readCodeSnippet =\n%q\nwant\n%q", got, want)
	}

	// Non-text / missing file -> empty.
	if s := readCodeSnippet(repo, "nope.bin", 1, 5); s != "" {
		t.Fatalf("non-text snippet = %q, want empty", s)
	}
}

func TestExtractHunkForLine(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n line1\n+added\n line2\n@@ -10,2 +12,3 @@\n line12\n+added2\n"
	lines := splitLines(patch)

	cases := []struct {
		line int
		want string
	}{
		{3, "@@ -1,3 +1,4 @@\n line1\n+added\n line2"},
		{13, "@@ -10,2 +12,3 @@\n line12\n+added2"},
	}
	for _, tc := range cases {
		got := joinLines(extractHunkForLine(lines, tc.line))
		if got != tc.want {
			t.Errorf("hunk for line %d =\n%q\nwant\n%q", tc.line, got, tc.want)
		}
	}
	// Line matching no hunk -> empty slice.
	if h := extractHunkForLine(lines, 99); len(h) != 0 {
		t.Fatalf("line 99 hunk = %v, want empty", h)
	}
}

func TestExtractDiffHunk(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n line1\n+added\n line2\n@@ -10,2 +12,3 @@\n line12\n+added2\n"
	l3, l13, l99 := 3, 13, 99

	if got := extractDiffHunk(map[string]string{"app.py": patch}, "app.py", &l3); got != "@@ -1,3 +1,4 @@\n line1\n+added\n line2" {
		t.Fatalf("line3 = %q", got)
	}
	if got := extractDiffHunk(map[string]string{"app.py": patch}, "app.py", &l13); got != "@@ -10,2 +12,3 @@\n line12\n+added2" {
		t.Fatalf("line13 = %q", got)
	}
	// No matching hunk -> whole patch (capped at 200 lines).
	wantWhole := "@@ -1,3 +1,4 @@\n line1\n+added\n line2\n@@ -10,2 +12,3 @@\n line12\n+added2"
	if got := extractDiffHunk(map[string]string{"app.py": patch}, "app.py", &l99); got != wantWhole {
		t.Fatalf("line99 = %q", got)
	}
	// Key stored with a/ prefix is found via the normalize-scan fallback.
	if got := extractDiffHunk(map[string]string{"a/app.py": patch}, "app.py", &l3); got != "@@ -1,3 +1,4 @@\n line1\n+added\n line2" {
		t.Fatalf("a/ key = %q", got)
	}
	// nil line -> whole patch.
	if got := extractDiffHunk(map[string]string{"app.py": patch}, "app.py", nil); got != wantWhole {
		t.Fatalf("nil line = %q", got)
	}
	// Missing file -> empty.
	if got := extractDiffHunk(map[string]string{}, "app.py", &l3); got != "" {
		t.Fatalf("missing = %q", got)
	}
}

func TestSplitExt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"foo.py", ".py"},
		{"a/b/foo.py", ".py"},
		{"foo.tar.gz", ".gz"},
		{".gitignore", ""}, // leading-dot filename has no extension (os.path.splitext)
		{"dir/.env", ""},
		{"noext", ""},
		{"a.b/c", ""},
	}
	for _, tc := range cases {
		if got := splitExt(tc.in); got != tc.want {
			t.Errorf("splitExt(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsTextFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "code.py", "print(1)\n")
	writeFile(t, repo, "data.bin", "x\x00y")
	writeFile(t, repo, "noext_text", "hello no null\n")
	writeFile(t, repo, "noext_binary", "he\x00llo")

	if !isTextFile(filepath.Join(repo, "code.py")) {
		t.Error(".py should be text")
	}
	if isTextFile(filepath.Join(repo, "data.bin")) {
		t.Error(".bin (unknown ext) should be non-text")
	}
	if !isTextFile(filepath.Join(repo, "noext_text")) {
		t.Error("extensionless null-free file should be text")
	}
	if isTextFile(filepath.Join(repo, "noext_binary")) {
		t.Error("extensionless file with null byte should be non-text")
	}
	if isTextFile(repo) {
		t.Error("directory should be non-text")
	}
}

func TestReEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pkg.mod", `pkg\.mod`},
		{"my-pkg", `my\-pkg`},
		{"plain", "plain"},
		{"a.b.c", `a\.b\.c`},
	}
	for _, tc := range cases {
		if got := reEscape(tc.in); got != tc.want {
			t.Errorf("reEscape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitLinesKeepEnds(t *testing.T) {
	got := splitLinesKeepEnds("a\nb\r\nc\rd")
	want := []string{"a\n", "b\r\n", "c\r", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keepends = %v, want %v", got, want)
	}
	// Trailing terminator produces no empty final element.
	if g := splitLinesKeepEnds("x\n"); !reflect.DeepEqual(g, []string{"x\n"}) {
		t.Fatalf("trailing nl = %v", g)
	}
	if g := splitLinesKeepEnds(""); len(g) != 0 {
		t.Fatalf("empty = %v", g)
	}
}

func TestSplitLines(t *testing.T) {
	got := splitLines("a\nb\r\nc\rd\n")
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitLines = %v, want %v", got, want)
	}
}

func TestTruncateChars(t *testing.T) {
	if got := truncateChars("hello", 3); got != "hel" {
		t.Errorf("ascii = %q", got)
	}
	if got := truncateChars("hello", 10); got != "hello" {
		t.Errorf("over = %q", got)
	}
	// Multi-byte: 3 runes, each 3 bytes.
	if got := truncateChars("αβγδ", 2); got != "αβ" {
		t.Errorf("multibyte = %q", got)
	}
}

func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"a", "", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupe = %v, want %v", got, want)
	}
	if got := dedupeStrings(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil dedupe should be non-nil empty, got %#v", got)
	}
}

// --- grep-dependent tests (fixtures use import-style so results are stable
// across grep flavors) ---

func TestFindFunctionCallers(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "deffile.py", "def target_fn():\n    return 1\n")
	writeFile(t, repo, "caller.py", "import deffile\n\ndef run():\n    return deffile.target_fn()\n")

	got := findFunctionCallers(context.Background(), repo, "target_fn", "deffile.py")
	want := []string{"caller.py:4\n1: import deffile\n2: \n3: def run():\n4:     return deffile.target_fn()"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callers =\n%#v\nwant\n%#v", got, want)
	}

	// Invalid identifier -> empty.
	if s := findFunctionCallers(context.Background(), repo, "1bad", ""); len(s) != 0 {
		t.Fatalf("invalid ident -> %v", s)
	}
}

func TestBuildImportContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "deffile.py", "def target_fn():\n    return 1\n")
	writeFile(t, repo, "caller.py", "import deffile\n\ndef run():\n    return deffile.target_fn()\n")
	writeFile(t, repo, "mymod.py", "import os\n")
	writeFile(t, repo, "uses_mymod.py", "import mymod\n")
	writeFile(t, repo, "solo.py", "from somewhere import thing\n")

	cases := []struct{ file, want string }{
		{"deffile.py", "IMPORTS: none\nIMPORTED BY: caller.py"},
		{"mymod.py", "IMPORTS: import os\nIMPORTED BY: uses_mymod.py"},
		{"solo.py", "IMPORTS: from somewhere import thing\nIMPORTED BY: none"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got := buildImportContext(context.Background(), repo, tc.file)
			if got != tc.want {
				t.Fatalf("import_context(%s) =\n%q\nwant\n%q", tc.file, got, tc.want)
			}
		})
	}
}

func TestBuildDimensionPack(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "lonely.py", "import os\nimport sys\n\ndef helper():\n    return os.getcwd()\n")
	writeFile(t, repo, "uses_lonely.py", "import lonely\n")

	got := BuildDimensionPack(context.Background(), repo, []string{"lonely.py"}, nil)
	want := "### lonely.py\n```\n1: import os\n2: import sys\n3: \n4: def helper():\n5:     return os.getcwd()\n```\n" +
		"_import/usage context:_ IMPORTS: import os, import sys\nIMPORTED BY: uses_lonely.py"
	if got != want {
		t.Fatalf("dimension pack =\n%q\nwant\n%q", got, want)
	}

	// Empty inputs -> empty.
	if s := BuildDimensionPack(context.Background(), "", []string{"x.py"}, nil); s != "" {
		t.Fatalf("empty repo -> %q", s)
	}
	if s := BuildDimensionPack(context.Background(), repo, nil, nil); s != "" {
		t.Fatalf("no targets -> %q", s)
	}
}

func TestExtractEvidenceForFindingsIntegration(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "deffile.py", "def target_fn():\n    return 1\n")
	writeFile(t, repo, "caller.py", "import deffile\n\ndef run():\n    return deffile.target_fn()\n")

	f := schemas.ReviewFinding{
		DimensionID:   "d",
		DimensionName: "D",
		FilePath:      "deffile.py",
		LineStart:     1,
		LineEnd:       2,
		Title:         "Bug in `target_fn`",
		Body:          "the function `target_fn` is wrong",
		Severity:      "important",
		Confidence:    0.9,
	}
	diff := map[string]string{"deffile.py": "@@ -1,2 +1,2 @@\n-def target_fn():\n+def target_fn(x):\n"}

	res, err := ExtractEvidenceForFindings(context.Background(), []schemas.ReviewFinding{f}, repo, diff, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 package, got %d", len(res))
	}
	pkg, ok := res["Bug in `target_fn`"]
	if !ok {
		t.Fatalf("missing key; keys=%v", keysOf(res))
	}

	if pkg.PrimaryCode != "1: def target_fn():\n2:     return 1" {
		t.Errorf("primary_code = %q", pkg.PrimaryCode)
	}
	if pkg.DiffHunk != "@@ -1,2 +1,2 @@\n-def target_fn():\n+def target_fn(x):" {
		t.Errorf("diff_hunk = %q", pkg.DiffHunk)
	}
	wantCallers := []string{"caller.py:4\n1: import deffile\n2: \n3: def run():\n4:     return deffile.target_fn()"}
	if !reflect.DeepEqual(pkg.CallerSnippets, wantCallers) {
		t.Errorf("caller_snippets = %#v", pkg.CallerSnippets)
	}
	if pkg.ImportContext != "IMPORTS: none\nIMPORTED BY: caller.py" {
		t.Errorf("import_context = %q", pkg.ImportContext)
	}
	// default_factory=list -> non-nil empty slices.
	if pkg.CrossRefSnippets == nil {
		t.Error("cross_ref_snippets must be non-nil []")
	}
	if pkg.RelatedCode != "" {
		t.Errorf("related_code = %q", pkg.RelatedCode)
	}
}

func TestExtractEvidenceEmpty(t *testing.T) {
	res, err := ExtractEvidenceForFindings(context.Background(), nil, "/tmp", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res) != 0 {
		t.Fatalf("empty findings -> %#v, want non-nil empty map", res)
	}
}

// --- semaphore bound + order preservation (via test seam) ---

func TestExtractEvidenceSemaphoreBoundAndOrder(t *testing.T) {
	const n = 40

	var current, maxSeen int64
	orig := extractForFindingFn
	t.Cleanup(func() { extractForFindingFn = orig })
	extractForFindingFn = func(_ context.Context, finding schemas.ReviewFinding, _ string, _ map[string]string, _ []string) EvidencePackage {
		cur := atomic.AddInt64(&current, 1)
		for {
			m := atomic.LoadInt64(&maxSeen)
			if cur <= m || atomic.CompareAndSwapInt64(&maxSeen, m, cur) {
				break
			}
		}
		time.Sleep(3 * time.Millisecond) // hold concurrency to observe the bound
		atomic.AddInt64(&current, -1)
		// Echo the title so we can verify identity/order in the result map.
		return EvidencePackage{FindingTitle: finding.Title, PrimaryCode: finding.Title}
	}

	findings := make([]schemas.ReviewFinding, n)
	for i := range findings {
		findings[i] = schemas.ReviewFinding{Title: title(i)}
	}

	res, err := ExtractEvidenceForFindings(context.Background(), findings, "/repo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := atomic.LoadInt64(&maxSeen); m > evidenceSemaphoreWeight {
		t.Fatalf("max concurrency %d exceeded semaphore weight %d", m, evidenceSemaphoreWeight)
	}
	if m := atomic.LoadInt64(&maxSeen); m < 2 {
		t.Fatalf("expected concurrency > 1, got %d (bound not exercised)", m)
	}
	// Every finding i must map to evidence built from finding i.
	for i := 0; i < n; i++ {
		pkg, ok := res[title(i)]
		if !ok {
			t.Fatalf("missing evidence for %s", title(i))
		}
		if pkg.PrimaryCode != title(i) {
			t.Fatalf("evidence for %s carried %q (mismatched finding)", title(i), pkg.PrimaryCode)
		}
	}
}

// TestExtractEvidenceTitleCollisionLastWins asserts the pre-indexed slice keeps
// input order so a duplicated title resolves to the LAST finding's evidence.
func TestExtractEvidenceTitleCollisionLastWins(t *testing.T) {
	orig := extractForFindingFn
	t.Cleanup(func() { extractForFindingFn = orig })
	extractForFindingFn = func(_ context.Context, finding schemas.ReviewFinding, _ string, _ map[string]string, _ []string) EvidencePackage {
		return EvidencePackage{FindingTitle: finding.Title, PrimaryCode: finding.Body}
	}

	findings := []schemas.ReviewFinding{
		{Title: "dup", Body: "first"},
		{Title: "dup", Body: "second"},
	}
	res, err := ExtractEvidenceForFindings(context.Background(), findings, "/repo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 key, got %d", len(res))
	}
	if res["dup"].PrimaryCode != "second" {
		t.Fatalf("collision resolved to %q, want last (%q)", res["dup"].PrimaryCode, "second")
	}
}

// --- helpers ---

func title(i int) string {
	return "f" + strconv.Itoa(i)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinLines(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}
