package reasoners

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// The reasoners must WRITE .pr-af-context files whose content is byte-identical
// to the JSON/markdown blocks the prompt builders render inline when small.
// These tests pin the two copies of the assembly logic together: for each
// evidence/patch context, the reasoner-side string must appear verbatim inside
// the built prompt (inline branch), and the oversized branch must write exactly
// that string to the expected path.

func sampleEvidence(title string) map[string]map[string]any {
	return map[string]map[string]any{
		title: {
			"finding_title":      title,
			"primary_code":       "def f():\n    return 1",
			"caller_snippets":    []any{"caller1()", "caller2()"},
			"cross_ref_snippets": []any{"xref1"},
			"diff_hunk":          "+return 1",
			"import_context":     "imports os",
			"related_code":       "def g(): pass",
		},
	}
}

func sampleFindings() []schemas.ReviewFinding {
	sugg := "fix it"
	return []schemas.ReviewFinding{
		{Title: "F1", Severity: "critical", FilePath: "a.go", LineStart: 3, LineEnd: 5,
			DimensionName: "Dim A", Body: "body1", Evidence: "ev1", Suggestion: &sugg,
			Confidence: 0.9, Tags: []string{"security"}},
		{Title: "F2", Severity: "suggestion", FilePath: "b.go", LineStart: 7,
			DimensionName: "Dim B", Body: "body2", Evidence: "ev2", Confidence: 0.6},
	}
}

// Contract: compoundFinderContext == the summary CompoundFinderPrompt renders
// inline ("Cluster context:\n<summary>").
func TestCompoundFinderContextMatchesBuilder(t *testing.T) {
	findings := sampleFindings()
	evMap := evidenceOMaps(sampleEvidence("F1"))
	content := compoundFinderContext(findings, evMap)
	prompt := prompts.CompoundFinderPrompt(findings, "", evMap)
	if !strings.Contains(prompt, "Cluster context:\n"+content) {
		t.Fatalf("assembled context diverges from the builder's inline rendering\ncontent: %s", content)
	}
	// Evidence must actually be embedded.
	if !strings.Contains(content, `"evidence_package": {"primary_code": "def f():\n    return 1"`) {
		t.Fatalf("evidence_package missing/malformed: %s", content)
	}
	if !strings.Contains(content, `"cluster_evidence": {"F1": {"finding_title": "F1"`) {
		t.Fatalf("cluster_evidence key order wrong: %s", content)
	}
}

// Contract: evidenceVerifierContext == the findings block
// EvidenceVerifierPrompt embeds directly.
func TestEvidenceVerifierContextMatchesBuilder(t *testing.T) {
	findings := sampleFindings()
	evMap := evidenceOMaps(sampleEvidence("F2"))
	content := evidenceVerifierContext(findings, evMap)
	prompt := prompts.EvidenceVerifierPrompt(findings, evMap, "ctx", "")
	if !strings.Contains(prompt, "## Findings to Verify\n\n"+content) {
		t.Fatalf("assembled context diverges from the builder\ncontent: %s", content)
	}
	if !strings.Contains(content, `"extracted_code": {"primary_code"`) {
		t.Fatalf("extracted_code missing: %s", content)
	}
}

// Contract: adversaryContext == the summary AdversaryPrompt renders inline
// ("Findings with ground-truth evidence:\n<summary>").
func TestAdversaryContextMatchesBuilder(t *testing.T) {
	findings := sampleFindings()
	evMap := evidenceOMaps(sampleEvidence("F1"))
	content := adversaryContext(findings, evMap)
	prompt := prompts.AdversaryPrompt(findings, 0.2, "", "", evMap)
	if !strings.Contains(prompt, "Findings with ground-truth evidence:\n"+content) {
		t.Fatalf("assembled context diverges from the builder\ncontent: %s", content)
	}
	if !strings.Contains(content, `"ground_truth": {"primary_code"`) {
		t.Fatalf("ground_truth missing: %s", content)
	}
}

// Contract: renderPatches(filterPairs(...)) == the block DeepenFindingsPrompt /
// ExtractObligationsPrompt render inline ("## Changed code (diffs)\n\n<text>").
func TestRenderPatchesMatchesBuilders(t *testing.T) {
	patches := OrderedPatches{
		{Key: "a.go", Val: "+added line"},
		{Key: "b.go", Val: ""},
		{Key: "c.go", Val: "-removed"},
	}
	text := renderPatches(filterPairs(patches))
	deepen := prompts.DeepenFindingsPrompt([]prompts.StrPair(patches), nil, "", "")
	if !strings.Contains(deepen, "## Changed code (diffs)\n\n"+text) {
		t.Fatalf("deepen prompt diverges from renderPatches:\n%s", text)
	}
	obligations := prompts.ExtractObligationsPrompt([]prompts.StrPair(patches), "", "")
	if !strings.Contains(obligations, "## Changed code (diffs)\n\n"+text) {
		t.Fatalf("obligations prompt diverges from renderPatches:\n%s", text)
	}
}

// Contract: an oversized meta context is written to
// <repo>/.pr-af-context/meta_<lens>_context.json with content identical to
// prompts.MetaContext, and the prompt references that path instead of the
// inline JSON.
func TestMetaSelectorWritesContextFile(t *testing.T) {
	repo := t.TempDir()
	// >8000 chars of diff patches pushes the context over the threshold.
	bigPatch := strings.Repeat("+x\n", 4000)
	in := MetaInput{
		Depth:       "standard",
		RepoPath:    repo,
		DiffPatches: OrderedPatches{{Key: "a.go", Val: bigPatch}},
	}
	h := &mockHarness{parseFail: true}
	if _, err := MetaSemantic(context.Background(), Deps{Harness: h}, in); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".pr-af-context", "meta_semantic_context.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("context file not written: %v", err)
	}
	want := prompts.MetaContext(in.Intake, in.Anatomy, []prompts.StrPair(in.DiffPatches), "")
	if string(b) != want {
		t.Fatal("context file content diverges from prompts.MetaContext")
	}
	if !strings.Contains(h.gotPrompt, "Full analysis context written to: "+path) {
		t.Fatal("prompt should reference the context file path")
	}
	if strings.Contains(h.gotPrompt, bigPatch) {
		t.Fatal("oversized context must not be inlined")
	}
}

// Contract: review_dimension writes oversized target-file diff patches and
// primed code to their .pr-af-context files, with the prompt referencing them.
func TestReviewDimensionWritesContextFiles(t *testing.T) {
	repo := t.TempDir()
	bigPatch := strings.Repeat("+line\n", 1500) // > 6000 chars
	bigPrimed := strings.Repeat("code\n", 1500) // > 6000 chars
	h := &mockHarness{parseFail: true}
	_, err := ReviewDimension(context.Background(), Deps{Harness: h}, ReviewDimensionInput{
		ReviewPrompt: "x",
		TargetFiles:  []string{"a.go"},
		RepoPath:     repo,
		MaxDepth:     2,
		DiffPatches:  map[string]string{"a.go": bigPatch, "other.go": "ignored"},
		PrimedCode:   bigPrimed,
	})
	if err != nil {
		t.Fatal(err)
	}
	diffPath := filepath.Join(repo, ".pr-af-context", "review_dimension_diff_patches.md")
	b, err := os.ReadFile(diffPath)
	if err != nil {
		t.Fatalf("diff patches file not written: %v", err)
	}
	if want := "### a.go\n```diff\n" + bigPatch + "\n```"; string(b) != want {
		t.Fatal("diff patches file content diverges")
	}
	primedPath := filepath.Join(repo, ".pr-af-context", "review_dimension_primed_code.md")
	pb, err := os.ReadFile(primedPath)
	if err != nil {
		t.Fatalf("primed code file not written: %v", err)
	}
	if string(pb) != bigPrimed {
		t.Fatal("primed code file content diverges")
	}
	if !strings.Contains(h.gotPrompt, "Full diff patches written to: "+diffPath) {
		t.Fatal("prompt should reference the diff patches file")
	}
	if !strings.Contains(h.gotPrompt, "context is written to: "+primedPath) {
		t.Fatal("prompt should reference the primed code file")
	}
}

// Contract: oversized deepen/obligation diffs are written to their files with
// exactly the rendered patch text.
func TestDeepenAndObligationsWriteDiffFiles(t *testing.T) {
	repo := t.TempDir()
	bigPatch := strings.Repeat("+line\n", 2000) // > 9000 chars rendered
	patches := OrderedPatches{{Key: "a.go", Val: bigPatch}}
	want := renderPatches(filterPairs(patches))

	h := &mockHarness{parseFail: true}
	if _, err := DeepenFindings(context.Background(), Deps{Harness: h}, DeepenInput{
		DiffPatches: patches, RepoPath: repo,
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(repo, ".pr-af-context", "deepen_diff.md"))
	if err != nil {
		t.Fatalf("deepen_diff.md not written: %v", err)
	}
	if string(b) != want {
		t.Fatal("deepen_diff.md content diverges from renderPatches")
	}
	if !strings.Contains(h.gotPrompt, "Changed-code diffs written to: ") {
		t.Fatal("deepen prompt should reference the diff file")
	}

	h = &mockHarness{parseFail: true}
	if _, err := ExtractObligations(context.Background(), Deps{Harness: h}, ExtractObligationsInput{
		DiffPatches: patches, RepoPath: repo,
	}); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(repo, ".pr-af-context", "obligations_diff.md"))
	if err != nil {
		t.Fatalf("obligations_diff.md not written: %v", err)
	}
	if string(b) != want {
		t.Fatal("obligations_diff.md content diverges from renderPatches")
	}
}

// Contract: an oversized compound-finder context is written to
// compound_cluster_findings.json with content identical to the builder's
// summary, and the prompt references the path.
func TestCompoundFinderWritesContextFile(t *testing.T) {
	repo := t.TempDir()
	big := strings.Repeat("x", 12000)
	ev := sampleEvidence("F1")
	ev["F1"]["primary_code"] = big // > 10000 rendered
	evMap := evidenceOMaps(ev)
	findings := sampleFindings()
	want := compoundFinderContext(findings, evMap)

	h := &mockHarness{parseFail: true}
	if _, err := CompoundFinderPhase(context.Background(), Deps{Harness: h}, CompoundFinderInput{
		ClusterFindings: findings, RepoPath: repo, EvidenceMap: ev,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".pr-af-context", "compound_cluster_findings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("compound context file not written: %v", err)
	}
	if string(b) != want {
		t.Fatal("compound context file diverges from the builder summary")
	}
	if !strings.Contains(h.gotPrompt, "Cluster findings and evidence written to: "+path) {
		t.Fatal("prompt should reference the context file")
	}
}

// Contract: evidenceOMaps preserves EvidencePackage.model_dump() key order and
// keeps `bool(ev_map)` truthiness (nil for empty).
func TestEvidenceOMapsOrderAndTruthiness(t *testing.T) {
	if evidenceOMaps(nil) != nil || evidenceOMaps(map[string]map[string]any{}) != nil {
		t.Fatal("empty evidence must convert to nil (has_evidence falsy)")
	}
	pkg := sampleEvidence("T")["T"]
	pkg["verification"] = map[string]any{
		"verification_notes": "n", "verified": true, "actual_behavior": "a",
	}
	om := orderedOMap(pkg, evidenceKeyOrder)
	got := prompts.PyJSON(om)
	want := `{"finding_title": "T", "primary_code": "def f():\n    return 1", ` +
		`"caller_snippets": ["caller1()", "caller2()"], "cross_ref_snippets": ["xref1"], ` +
		`"diff_hunk": "+return 1", "import_context": "imports os", "related_code": "def g(): pass", ` +
		`"verification": {"verified": true, "actual_behavior": "a", "verification_notes": "n"}}`
	if got != want {
		t.Fatalf("evidence OMap rendering:\n got  %s\n want %s", got, want)
	}
}
