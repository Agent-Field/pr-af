package prompts

import (
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func TestAnatomyGolden(t *testing.T) {
	// anatomy_A: prA metadata + intake_dict(); diff-engine derived clusters/stats
	// transcribed from the fixture (root cluster over all 3 metadata files).
	clustersA := []schemas.ChangeCluster{
		{ID: "cluster_0", Name: "root", Files: []string{"client.py", "retry.py", "client.test.ts", "README.md"}, PrimaryLanguage: "python", Description: ""},
	}
	statsA := schemas.DiffStats{TotalFiles: 4, FilesAdded: 2, FilesModified: 2, TestFilesChanged: 1, TestToCodeRatio: 1.0 / 3.0}
	filesA := []schemas.FileChange{
		{Path: "client.py", Status: "modified"},
		{Path: "retry.py", Status: "added"},
		{Path: "client.test.ts", Status: "added"},
		{Path: "README.md", Status: "modified"},
	}
	assertGolden(t, "anatomy_A", AnatomyPrompt(
		intakeFix(nil),
		"Add retry logic to HTTP client",
		"Wraps the client in a retry decorator with exponential backoff.\nCloses #42.",
		[]string{"enhancement", "backend"},
		clustersA, statsA, 0, filesA,
	))

	// anatomy_B: prB minimal, empty changed_files -> empty derivations.
	assertGolden(t, "anatomy_B", AnatomyPrompt(
		intakeFix(func(in *schemas.IntakeResult) {
			in.PrSummary = "Bumps a dependency."
			in.AreasTouched = []string{"config"}
		}),
		"Bump dep", "", nil,
		[]schemas.ChangeCluster{}, schemas.DiffStats{}, 0, []schemas.FileChange{},
	))
}

func TestPlanningGolden(t *testing.T) {
	assertGolden(t, "planning_A", PlanningPrompt(
		intakeFix(func(in *schemas.IntakeResult) { in.AreasTouched = []string{"api", "config"} }),
		anatA(), "deep", []string{"focus on idempotency", "ignore style"},
	))
	assertGolden(t, "planning_B", PlanningPrompt(
		intakeFix(nil), anatomyFix(nil), "standard", []string{},
	))
}

func TestMetaGolden(t *testing.T) {
	patches := []StrPair{{Key: "client.py", Val: "@@ -1,3 +1,5 @@\n+def retry():\n+    pass"}}
	lenses := []struct {
		name string
		fn   func(context, repoPath, depth string) string
	}{
		{"semantic", MetaSemanticPrompt},
		{"mechanical", MetaMechanicalPrompt},
		{"systemic", MetaSystemicPrompt},
	}
	for _, l := range lenses {
		ctxA := MetaContext(intakeFix(nil), anatA(), patches, "tone down the nitpicks")
		assertGolden(t, "meta_"+l.name+"_A", l.fn(ctxA, "", "deep"))
		ctxB := MetaContext(intakeFix(nil), anatomyFix(nil), nil, "")
		assertGolden(t, "meta_"+l.name+"_B", l.fn(ctxB, "", "standard"))
	}
	// C: large-context file-write branch (semantic only).
	bigPatches := []StrPair{{Key: "client.py", Val: bigFiller("patch", 9000)}}
	ctxC := MetaContext(intakeFix(nil), anatA(), bigPatches, "focus on auth")
	assertGolden(t, "meta_semantic_C", MetaSemanticPrompt(ctxC, fixtureRepo, "deep"))
}

func TestCoverageGolden(t *testing.T) {
	assertGolden(t, "coverage_gate_system", CoverageGateSystem)
	covan := anatomyFix(func(a *schemas.AnatomyResult) {
		a.Clusters = []schemas.ChangeCluster{
			clusterFix("cluster_0", "Client core", []string{"client.py"}, ""),
			clusterFix("cluster_1", "Tests", []string{"t.py"}, ""),
		}
		a.RiskSurfaces = []string{"error propagation"}
	})
	assertGolden(t, "coverage_gate_A", CoverageGatePrompt(covan, []string{"cluster_0"}, []string{"Semantic: error paths", "Mechanical"}))
	assertGolden(t, "coverage_gate_B", CoverageGatePrompt(covan, []string{}, nil))
}
