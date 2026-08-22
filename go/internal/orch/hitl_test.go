package orch

// V9: Orchestrator HITL control flow with the heavy phases stubbed via seams
// (mirrors tests/test_orchestrator_hitl.py). HITL off posts directly (gate never
// consulted); approve-subset posts only the selected findings; rerun threads
// feedback then posts; zero findings skips the gate and posts nothing; reject
// posts nothing; a rerun past the revision cap posts nothing.

import (
	"context"
	"testing"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/hitl"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// fakeApp records notes; the harness/AI/pause surfaces panic because the stubbed
// phases must never reach a live model in these control-flow tests.
type fakeApp struct {
	notes    []string
	noteTags [][]string
}

func (f *fakeApp) Note(_ context.Context, message string, tags ...string) {
	f.notes = append(f.notes, message)
	f.noteTags = append(f.noteTags, append([]string(nil), tags...))
}
func (f *fakeApp) Harness(context.Context, string, map[string]any, any, harness.Options) (*harness.Result, error) {
	panic("Harness not expected in HITL control-flow test")
}
func (f *fakeApp) AI(context.Context, string, ...ai.Option) (*ai.Response, error) {
	panic("AI not expected in HITL control-flow test")
}
func (f *fakeApp) Pause(context.Context, agent.PauseOptions) (*agent.ApprovalResult, error) {
	panic("Pause not expected in HITL control-flow test")
}

type capturedOutput struct {
	findings []schemas.ScoredFinding
	post     bool
}

func scoredFinding(id string) schemas.ScoredFinding {
	return schemas.ScoredFinding{
		ID:            id,
		DimensionID:   "d1",
		DimensionName: "dim",
		FilePath:      "src/foo.py",
		LineStart:     10,
		LineEnd:       10,
		DiffSide:      "RIGHT",
		Severity:      "important",
		Title:         "title-" + id,
		Body:          "body",
		Confidence:    0.5,
	}
}

func strPtr(s string) *string { return &s }

func makeHITLOrchestrator(
	findings []schemas.ScoredFinding,
	hitlOn bool,
	decisions []hitl.ReviewDecision,
) (*Orchestrator, *capturedOutput, *[]string, *int) {
	app := &fakeApp{}
	in := schemas.ReviewInput{PrURL: strPtr("https://github.com/o/r/pull/1"), Depth: "auto"}
	o := New(Deps{App: app}, in, config.DefaultReviewConfig())

	cap := &capturedOutput{}
	feedbacks := &[]string{}
	callCount := 0

	o.runIntakeFn = func(context.Context) (schemas.IntakeResult, error) {
		o.prData = &schemas.GitHubPRData{Owner: "o", Repo: "r", Number: 1, Title: "t"}
		return schemas.IntakeResult{PrSummary: "adds caching", PrType: "feature", Complexity: "low"}, nil
	}
	o.runAnatomyFn = func(context.Context, schemas.IntakeResult) (schemas.AnatomyResult, error) {
		return schemas.AnatomyResult{}, nil
	}
	o.resolveDepthFn = func(schemas.IntakeResult) string { return "standard" }
	o.runReviewPhasesFn = func(_ context.Context, _ schemas.IntakeResult, _ schemas.AnatomyResult, _, feedback string) (schemas.ReviewPlan, []schemas.ScoredFinding, error) {
		*feedbacks = append(*feedbacks, feedback)
		return schemas.ReviewPlan{}, append([]schemas.ScoredFinding(nil), findings...), nil
	}
	o.generateOutputFn = func(_ context.Context, scored []schemas.ScoredFinding, _ schemas.IntakeResult, _ schemas.AnatomyResult, _ schemas.ReviewPlan, post bool) (schemas.ReviewResult, error) {
		cap.findings = append([]schemas.ScoredFinding(nil), scored...)
		cap.post = post
		return schemas.ReviewResult{Summary: schemas.ReviewSummary{TotalFindings: len(scored)}}, nil
	}
	o.cleanupFn = func() {}
	o.approvalWebhookFn = func() *string { return nil }
	if hitlOn {
		o.buildHaxClientFn = func() *hitl.HaxClient { return &hitl.HaxClient{} }
	} else {
		o.buildHaxClientFn = func() *hitl.HaxClient { return nil }
	}
	o.requestApprovalFn = func(context.Context, hitl.RequestReviewApprovalArgs) hitl.ReviewDecision {
		d := decisions[callCount]
		callCount++
		return d
	}
	return o, cap, feedbacks, &callCount
}

func idSetOf(findings []schemas.ScoredFinding) map[string]struct{} {
	s := map[string]struct{}{}
	for _, f := range findings {
		s[f.ID] = struct{}{}
	}
	return s
}

func TestHITLOffPostsDirectly(t *testing.T) {
	findings := []schemas.ScoredFinding{scoredFinding("f1"), scoredFinding("f2")}
	o, cap, _, calls := makeHITLOrchestrator(findings, false, nil)
	if _, err := o.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cap.post {
		t.Error("expected post=true")
	}
	got := idSetOf(cap.findings)
	if len(got) != 2 || !contains(got, "f1") || !contains(got, "f2") {
		t.Errorf("posted findings = %v, want {f1,f2}", got)
	}
	if *calls != 0 {
		t.Errorf("gate consulted %d times, want 0", *calls)
	}
}

func TestApproveSubsetPostsOnlySelected(t *testing.T) {
	findings := []schemas.ScoredFinding{scoredFinding("f1"), scoredFinding("f2"), scoredFinding("f3")}
	decision := hitl.ReviewDecision{Action: hitl.ActionPost, SelectedFindingIDs: map[string]struct{}{"f1": {}, "f3": {}}}
	o, cap, _, _ := makeHITLOrchestrator(findings, true, []hitl.ReviewDecision{decision})
	if _, err := o.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cap.post {
		t.Error("expected post=true")
	}
	got := idSetOf(cap.findings)
	if len(got) != 2 || !contains(got, "f1") || !contains(got, "f3") {
		t.Errorf("posted findings = %v, want {f1,f3}", got)
	}
}

func TestRerunThenPostThreadsFeedback(t *testing.T) {
	findings := []schemas.ScoredFinding{scoredFinding("f1")}
	decisions := []hitl.ReviewDecision{
		{Action: hitl.ActionRerun, Instructions: "too aggressive, tone it down"},
		{Action: hitl.ActionPost, SelectedFindingIDs: map[string]struct{}{"f1": {}}},
	}
	o, cap, feedbacks, calls := makeHITLOrchestrator(findings, true, decisions)
	if _, err := o.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Errorf("asked %d times, want 2", *calls)
	}
	if len(*feedbacks) != 2 || (*feedbacks)[0] != "" {
		t.Errorf("feedbacks = %v, want first empty", *feedbacks)
	}
	if !contains2((*feedbacks)[1], "tone it down") {
		t.Errorf("second feedback = %q, want to contain 'tone it down'", (*feedbacks)[1])
	}
	if !cap.post {
		t.Error("expected post=true")
	}
}

func TestHITLZeroFindingsSkipsGate(t *testing.T) {
	o, cap, _, calls := makeHITLOrchestrator(nil, true, nil)
	if _, err := o.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cap.post {
		t.Error("expected post=false for zero findings")
	}
	if *calls != 0 {
		t.Errorf("gate consulted %d times, want 0", *calls)
	}
}

func TestRejectPostsNothing(t *testing.T) {
	findings := []schemas.ScoredFinding{scoredFinding("f1")}
	decision := hitl.ReviewDecision{Action: hitl.ActionReject, DecisionRaw: "rejected"}
	o, cap, _, _ := makeHITLOrchestrator(findings, true, []hitl.ReviewDecision{decision})
	if _, err := o.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cap.post {
		t.Error("expected post=false for reject")
	}
}

func TestRerunPastCapPostsNothing(t *testing.T) {
	findings := []schemas.ScoredFinding{scoredFinding("f1")}
	decisions := make([]hitl.ReviewDecision, 5)
	for i := range decisions {
		decisions[i] = hitl.ReviewDecision{Action: hitl.ActionRerun, Instructions: "again"}
	}
	o, cap, _, calls := makeHITLOrchestrator(findings, true, decisions)
	o.config.HITL.MaxReviewRevisions = 2 // 3 prompts (iters 0,1,2) then no post
	if _, err := o.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cap.post {
		t.Error("expected post=false past the revision cap")
	}
	if *calls != 3 {
		t.Errorf("asked %d times, want 3", *calls)
	}
}

func contains2(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
