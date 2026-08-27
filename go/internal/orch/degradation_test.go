package orch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/evidence"
	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func degradationOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	cfg := config.DefaultReviewConfig()
	cfg.Comments.PolishEnabled = false
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{DryRun: true}, cfg)
	o.prData = &schemas.GitHubPRData{}
	o.runIntakeFn = func(context.Context) (schemas.IntakeResult, error) {
		return schemas.IntakeResult{PrSummary: "summary", PrType: "feature", Complexity: "low"}, nil
	}
	o.runAnatomyFn = func(context.Context, schemas.IntakeResult) (schemas.AnatomyResult, error) {
		return schemas.AnatomyResult{}, nil
	}
	o.resolveDepthFn = func(schemas.IntakeResult) string { return "standard" }
	o.cleanupFn = func() {}
	return o
}

func degradationPlan(n int) schemas.ReviewPlan {
	dims := make([]schemas.ReviewDimension, n)
	for i := range dims {
		dims[i] = schemas.ReviewDimension{ID: string(rune('a' + i)), Name: "dimension", ReviewPrompt: "review", TargetFiles: []string{"a.go"}}
	}
	return schemas.ReviewPlan{Dimensions: dims}
}

func TestRunFailsBeforeOutputWhenAllDimensionsFailSchemaParsing(t *testing.T) {
	o := degradationOrchestrator(t)
	var outputCalls atomic.Int32
	o.rfns.reviewDim = func(context.Context, reasoners.Deps, reasoners.ReviewDimensionInput) (map[string]any, error) {
		return map[string]any{"schema_parse_failed": true}, nil
	}
	o.runReviewPhasesFn = func(ctx context.Context, _ schemas.IntakeResult, _ schemas.AnatomyResult, _, feedback string) (schemas.ReviewPlan, []schemas.ScoredFinding, error) {
		plan := degradationPlan(2)
		_, err := o.collectParallelReview(ctx, plan, feedback)
		return plan, nil, err
	}
	o.generateOutputFn = func(context.Context, []schemas.ScoredFinding, schemas.IntakeResult, schemas.AnatomyResult, schemas.ReviewPlan, bool) (schemas.ReviewResult, error) {
		outputCalls.Add(1)
		return schemas.ReviewResult{}, nil
	}

	_, err := o.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "all review dimensions failed schema parsing (failed dimensions: 2)") {
		t.Fatalf("Run error = %v", err)
	}
	if outputCalls.Load() != 0 {
		t.Fatalf("generateOutput calls = %d, want 0", outputCalls.Load())
	}
}

func TestRunMetaSelectorsRejectsZeroDimensionPlan(t *testing.T) {
	o := degradationOrchestrator(t)
	o.rfns.metaSemantic = emptyMetaSelector("semantic")
	o.rfns.metaMechanical = emptyMetaSelector("mechanical")
	o.rfns.metaSystemic = emptyMetaSelector("systemic")

	_, err := o.runMetaSelectors(
		context.Background(),
		schemas.IntakeResult{},
		schemas.AnatomyResult{},
		"standard",
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "selected zero review dimensions") {
		t.Fatalf("runMetaSelectors error = %v", err)
	}
}

func TestRunStopsBeforeReviewAndOutputWhenSelectorsProduceNoDimensions(t *testing.T) {
	o := degradationOrchestrator(t)
	o.rfns.metaSemantic = emptyMetaSelector("semantic")
	o.rfns.metaMechanical = emptyMetaSelector("mechanical")
	o.rfns.metaSystemic = emptyMetaSelector("systemic")
	var reviewerCalls atomic.Int32
	var outputCalls atomic.Int32
	o.rfns.reviewDim = func(context.Context, reasoners.Deps, reasoners.ReviewDimensionInput) (map[string]any, error) {
		reviewerCalls.Add(1)
		return map[string]any{"findings": []any{}}, nil
	}
	o.generateOutputFn = func(context.Context, []schemas.ScoredFinding, schemas.IntakeResult, schemas.AnatomyResult, schemas.ReviewPlan, bool) (schemas.ReviewResult, error) {
		outputCalls.Add(1)
		return schemas.ReviewResult{}, nil
	}

	_, err := o.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "selected zero review dimensions") {
		t.Fatalf("Run error = %v", err)
	}
	if reviewerCalls.Load() != 0 {
		t.Fatalf("reviewer calls = %d, want 0", reviewerCalls.Load())
	}
	if outputCalls.Load() != 0 {
		t.Fatalf("output calls = %d, want 0", outputCalls.Load())
	}
}

func TestReviewDimensionCompletionRejectsZeroAttempts(t *testing.T) {
	o := degradationOrchestrator(t)
	for _, phase := range requiredPreOutputPhases {
		o.markPhaseCompleted(phase)
	}
	err := o.validateReviewCompletion(degradationPlan(1))
	if err == nil || !strings.Contains(err.Error(), "no review dimensions were attempted") {
		t.Fatalf("validateReviewCompletion error = %v", err)
	}
}

func TestBudgetSkipCannotBecomeAZeroAttemptSuccess(t *testing.T) {
	o := degradationOrchestrator(t)
	o.clock = func() time.Duration { return 2 * time.Hour }

	_, err := o.collectParallelReview(context.Background(), degradationPlan(1), "")
	if err == nil || !strings.Contains(err.Error(), "no review dimensions were attempted") {
		t.Fatalf("collectParallelReview error = %v", err)
	}
}

func TestCoverageBudgetSkipFailsAfterAParseableReview(t *testing.T) {
	o := degradationOrchestrator(t)
	o.clock = func() time.Duration { return 2 * time.Hour }
	o.recordDimensionAttempt()
	o.recordDimensionResult(false)

	_, _, err := o.runCoverageLoop(
		context.Background(),
		degradationPlan(1),
		schemas.AnatomyResult{},
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "before coverage") {
		t.Fatalf("runCoverageLoop error = %v", err)
	}
}

func TestConsistencyExtractionFailurePropagates(t *testing.T) {
	o := degradationOrchestrator(t)
	o.prData = &schemas.GitHubPRData{
		ChangedFiles: []schemas.ChangedFile{{Path: "a.go", Patch: "@@ -1 +1 @@\n-old\n+new"}},
	}
	want := &harnessx.StructuredOutputError{Diagnostic: "missing obligations"}
	o.rfns.extractOblig = func(context.Context, reasoners.Deps, reasoners.ExtractObligationsInput) (map[string]any, error) {
		return nil, want
	}

	_, err := o.runConsistencyVerify(context.Background(), nil)
	if !errors.Is(err, want) {
		t.Fatalf("runConsistencyVerify error = %v, want %v", err, want)
	}
}

func TestConsistencyVerifierFailurePropagates(t *testing.T) {
	o := degradationOrchestrator(t)
	o.prData = &schemas.GitHubPRData{
		ChangedFiles: []schemas.ChangedFile{{Path: "a.go", Patch: "@@ -1 +1 @@\n-old\n+new"}},
	}
	o.rfns.extractOblig = func(context.Context, reasoners.Deps, reasoners.ExtractObligationsInput) (map[string]any, error) {
		return map[string]any{"obligations": []any{map[string]any{"id": "one"}}}, nil
	}
	want := &harnessx.StructuredOutputError{Diagnostic: "missing verdict"}
	o.rfns.verifyOblig = func(context.Context, reasoners.Deps, reasoners.VerifyObligationInput) (map[string]any, error) {
		return nil, want
	}

	_, err := o.runConsistencyVerify(context.Background(), nil)
	if !errors.Is(err, want) {
		t.Fatalf("runConsistencyVerify error = %v, want %v", err, want)
	}
}

func TestCompoundFinderFailurePropagates(t *testing.T) {
	o := degradationOrchestrator(t)
	want := &harnessx.StructuredOutputError{Diagnostic: "missing compound findings"}
	o.rfns.compoundFinder = func(context.Context, reasoners.Deps, reasoners.CompoundFinderInput) (map[string]any, error) {
		return nil, want
	}
	findings := []schemas.ReviewFinding{
		{Title: "first", FilePath: "src/a.go", Tags: []string{"correctness"}},
		{Title: "second", FilePath: "src/a.go", Tags: []string{"correctness"}},
	}

	_, err := o.runCompoundAnalysis(context.Background(), findings, nil)
	if !errors.Is(err, want) {
		t.Fatalf("runCompoundAnalysis error = %v, want %v", err, want)
	}
}

func TestCompoundFinderFailureCancelsSiblingClusters(t *testing.T) {
	o := degradationOrchestrator(t)
	var siblingCancelled atomic.Bool
	o.rfns.compoundFinder = func(ctx context.Context, _ reasoners.Deps, in reasoners.CompoundFinderInput) (map[string]any, error) {
		if strings.HasPrefix(in.ClusterFindings[0].FilePath, "src/") {
			return nil, &harnessx.StructuredOutputError{Diagnostic: "failed source cluster"}
		}
		<-ctx.Done()
		siblingCancelled.Store(true)
		return nil, ctx.Err()
	}
	findings := []schemas.ReviewFinding{
		{Title: "source one", FilePath: "src/a.go"},
		{Title: "source two", FilePath: "src/b.go"},
		{Title: "test one", FilePath: "test/a.go"},
		{Title: "test two", FilePath: "test/b.go"},
	}
	if clusters := selectCompoundClusters(findings, nil, o.config.Budget.MaxCrossRefDeepDives); len(clusters) < 2 {
		t.Fatalf("test requires at least two clusters, got %d", len(clusters))
	}

	if _, err := o.runCompoundAnalysis(context.Background(), findings, nil); err == nil {
		t.Fatal("runCompoundAnalysis unexpectedly succeeded")
	}
	if !siblingCancelled.Load() {
		t.Fatal("compound failure did not cancel the sibling cluster")
	}
}

func TestAdversaryCannotReturnAnEmptyResultForNonemptyFindings(t *testing.T) {
	o := degradationOrchestrator(t)
	o.rfns.adversary = func(context.Context, reasoners.Deps, reasoners.AdversaryInput) (map[string]any, error) {
		return map[string]any{"results": []any{}}, nil
	}

	_, err := o.runParallelAdversary(
		context.Background(),
		[]schemas.ReviewFinding{{Title: "missing adversary verdict"}},
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "omitted finding") {
		t.Fatalf("runParallelAdversary error = %v", err)
	}
}

func TestAdversaryFailsWhenTheBatchCapWouldOmitFindings(t *testing.T) {
	o := degradationOrchestrator(t)
	findings := make([]schemas.ReviewFinding, adversaryBatchSize*maxAdversaryBatch+1)
	for i := range findings {
		findings[i].Title = fmt.Sprintf("finding-%d", i)
	}

	_, err := o.runParallelAdversary(context.Background(), findings, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeding the fail-closed limit") {
		t.Fatalf("runParallelAdversary error = %v", err)
	}
}

func TestAdversaryRejectsDuplicateFindingTitles(t *testing.T) {
	o := degradationOrchestrator(t)
	var calls atomic.Int32
	o.rfns.adversary = func(context.Context, reasoners.Deps, reasoners.AdversaryInput) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"results": []any{
			map[string]any{"finding_title": "duplicate", "verdict": "confirmed"},
			map[string]any{"finding_title": "duplicate", "verdict": "challenged"},
		}}, nil
	}

	_, err := o.runParallelAdversary(
		context.Background(),
		[]schemas.ReviewFinding{{Title: "duplicate"}, {Title: "duplicate"}},
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate finding title") {
		t.Fatalf("runParallelAdversary error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("adversary calls = %d, want 0", calls.Load())
	}
}

func TestEvidenceVerifierCannotReturnAnEmptyResultForEligibleFindings(t *testing.T) {
	o := degradationOrchestrator(t)
	o.rfns.evidenceVerify = func(context.Context, reasoners.Deps, reasoners.EvidenceVerifierInput) (map[string]any, error) {
		return map[string]any{"verified_findings": []any{}}, nil
	}
	finding := schemas.ReviewFinding{Title: "missing evidence verdict", Severity: "important"}

	_, _, err := o.runEvidenceVerification(
		context.Background(),
		[]schemas.ReviewFinding{finding},
		map[string]evidence.EvidencePackage{finding.Title: {}},
	)
	if err == nil || !strings.Contains(err.Error(), "omitted finding") {
		t.Fatalf("runEvidenceVerification error = %v", err)
	}
}

func TestEvidenceVerificationRejectsDuplicateFindingTitles(t *testing.T) {
	o := degradationOrchestrator(t)
	var calls atomic.Int32
	o.rfns.evidenceVerify = func(context.Context, reasoners.Deps, reasoners.EvidenceVerifierInput) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"verified_findings": []any{
			map[string]any{"title": "duplicate", "verified": true},
			map[string]any{"title": "duplicate", "verified": false},
		}}, nil
	}
	findings := []schemas.ReviewFinding{
		{Title: "duplicate", Severity: "important"},
		{Title: "duplicate", Severity: "critical"},
	}

	_, _, err := o.runEvidenceVerification(
		context.Background(),
		findings,
		map[string]evidence.EvidencePackage{"duplicate": {}},
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate finding title") {
		t.Fatalf("runEvidenceVerification error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("evidence verifier calls = %d, want 0", calls.Load())
	}
}

func TestEvidenceVerificationUsesItsOwnCostBucket(t *testing.T) {
	o := degradationOrchestrator(t)
	o.rfns.evidenceVerify = func(context.Context, reasoners.Deps, reasoners.EvidenceVerifierInput) (map[string]any, error) {
		return map[string]any{
			"verified_findings": []any{map[string]any{"title": "costed", "verified": true}},
			"cost_usd":          0.25,
		}, nil
	}
	finding := schemas.ReviewFinding{Title: "costed", Severity: "important"}

	if _, _, err := o.runEvidenceVerification(
		context.Background(),
		[]schemas.ReviewFinding{finding},
		map[string]evidence.EvidencePackage{finding.Title: {}},
	); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	evidenceCost := o.costBreakdown["evidence_verification"]
	adversaryCost := o.costBreakdown["adversary"]
	o.mu.Unlock()
	if evidenceCost != 0.25 || adversaryCost != 0 {
		t.Fatalf("cost buckets evidence=%v adversary=%v", evidenceCost, adversaryCost)
	}
}

func TestReviewDimensionCompletionAcceptsExplicitCleanReview(t *testing.T) {
	o := degradationOrchestrator(t)
	for _, phase := range requiredPreOutputPhases {
		o.markPhaseCompleted(phase)
	}
	o.recordDimensionAttempt()
	o.recordDimensionResult(false)
	if err := o.validateReviewCompletion(degradationPlan(1)); err != nil {
		t.Fatalf("validateReviewCompletion error = %v", err)
	}
}

func TestReviewCompletionRejectsIncompleteConditionalPhaseAccounting(t *testing.T) {
	o := degradationOrchestrator(t)
	for _, phase := range requiredPreOutputPhases {
		o.markPhaseCompleted(phase)
	}
	o.recordDimensionAttempt()
	o.recordDimensionResult(false)
	o.recordAdversaryEligibility(1)

	err := o.validateReviewCompletion(degradationPlan(1))
	if err == nil || !strings.Contains(err.Error(), "adversary accounting is incomplete") {
		t.Fatalf("validateReviewCompletion error = %v", err)
	}
}

func TestReviewDimensionCompletionRequiresEveryCorePhase(t *testing.T) {
	o := degradationOrchestrator(t)
	for _, phase := range requiredPreOutputPhases {
		if phase != "coverage" {
			o.markPhaseCompleted(phase)
		}
	}
	o.recordDimensionAttempt()
	o.recordDimensionResult(false)

	err := o.validateReviewCompletion(degradationPlan(1))
	if err == nil || !strings.Contains(err.Error(), `required review phase "coverage" did not complete`) {
		t.Fatalf("validateReviewCompletion error = %v", err)
	}
}

func emptyMetaSelector(lens string) func(context.Context, reasoners.Deps, reasoners.MetaInput) (map[string]any, error) {
	return func(context.Context, reasoners.Deps, reasoners.MetaInput) (map[string]any, error) {
		return map[string]any{
			"lens":       lens,
			"dimensions": []any{},
			"confidence": 0.7,
			"rationale":  "",
		}, nil
	}
}

func TestMixedAndCleanDimensionsExposeOnlyActualDegradation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed int
		want   string
	}{
		{name: "mixed", failed: 1, want: "> Degraded dimensions: 1"},
		{name: "clean empty", failed: 0, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := degradationOrchestrator(t)
			for _, phase := range requiredPreOutputPhases {
				o.markPhaseCompleted(phase)
			}
			var calls atomic.Int32
			o.rfns.reviewDim = func(context.Context, reasoners.Deps, reasoners.ReviewDimensionInput) (map[string]any, error) {
				n := calls.Add(1)
				return map[string]any{
					"schema_parse_failed": n <= int32(tc.failed),
					"findings":            []any{},
					"sub_reviews":         []any{},
				}, nil
			}
			o.resetDimensionStats()
			plan := degradationPlan(3)
			if _, err := o.collectParallelReview(context.Background(), plan, ""); err != nil {
				t.Fatal(err)
			}
			result, err := o.generateOutput(context.Background(), nil, schemas.IntakeResult{PrSummary: "summary"}, schemas.AnatomyResult{}, plan, false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Metadata.DegradedDimensions != tc.failed {
				t.Fatalf("degraded_dimensions = %d, want %d", result.Metadata.DegradedDimensions, tc.failed)
			}
			if tc.want != "" && !strings.Contains(result.Review.Body, tc.want) {
				t.Fatalf("review body missing %q", tc.want)
			}
			if tc.want == "" && strings.Contains(result.Review.Body, "Degraded dimensions:") {
				t.Fatalf("clean body unexpectedly degraded: %s", result.Review.Body)
			}
		})
	}
}
