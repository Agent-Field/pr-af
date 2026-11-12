package orch

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"
	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/hitl"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

type paidReviewApp struct {
	reviewFixtureApp
	harnessCalls, aiCalls atomic.Int32
	costMultiplier        float64
}

func (a *paidReviewApp) Harness(ctx context.Context, prompt string, schema map[string]any, dest any, opts harness.Options) (*harness.Result, error) {
	res, err := a.reviewFixtureApp.Harness(ctx, prompt, schema, dest, opts)
	payload := ""
	switch reflect.TypeOf(dest).Elem().Name() {
	case "reviewFindingsResult":
		payload = `{"findings":[{"title":"fixture finding","body":"Check the changed behavior.","file_path":"a.go","line_start":1,"line_end":1,"severity":"important","confidence":0.95}],"sub_reviews":[]}`
	case "obligationsResult":
		payload = `{"obligations":[{"id":"one","where":"a.go:1","relies_on":"b.go:1","property":"consistent"}]}`
	}
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), dest); err != nil {
			return nil, err
		}
	}
	if res != nil {
		cost := .001
		if a.costMultiplier != 0 {
			cost *= a.costMultiplier
		}
		res.CostUSD = &cost
		a.harnessCalls.Add(1)
	}
	return res, err
}
func (a *paidReviewApp) AI(_ context.Context, _ string, opts ...ai.Option) (*ai.Response, error) {
	var req ai.Request
	for _, opt := range opts {
		if err := opt(&req); err != nil {
			return nil, err
		}
	}
	text := "polished fixture comment"
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			switch part.Text {
			case prompts.IntakeGateSystem:
				text = `{"confident":true,"pr_type":"feature","complexity":"standard"}`
			case prompts.CoverageGateSystem:
				text = `{"fully_covered":true,"confident":true,"gap_descriptions":[]}`
			case prompts.MergeGateSystem:
				text = `{"blocking":false,"reason":"advisory"}`
			}
		}
	}
	cost := .002
	if a.costMultiplier != 0 {
		cost *= a.costMultiplier
	}
	a.aiCalls.Add(1)
	return &ai.Response{Usage: &ai.Usage{Cost: &cost}, Choices: []ai.Choice{{Message: ai.Message{Content: []ai.ContentPart{{Type: "text", Text: text}}}}}}, nil
}

func TestReviewReportsAllSuppliedSpend(t *testing.T) {
	for _, earlyGate := range []bool{false, true} {
		t.Run(map[bool]string{false: "streamed", true: "early gate"}[earlyGate], func(t *testing.T) {
			app := &paidReviewApp{}
			cfg := config.DefaultReviewConfig()
			cfg.Comments.PostWorthinessGate = earlyGate
			gh := &reviewFixtureGH{}
			o := New(Deps{App: app, GH: gh}, schemas.ReviewInput{PrURL: strPtr("https://github.com/o/r/pull/1"), Depth: "standard"}, cfg)
			o.buildHaxClientFn = func() *hitl.HaxClient { return nil }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := o.Run(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := float64(app.harnessCalls.Load())*.001 + float64(app.aiCalls.Load())*.002
			got := result.Metadata.Budget["total_cost_usd"].(float64)
			if math.Abs(got-want) > 1e-9 || math.Abs(result.Summary.CostUSD-want) > 1e-9 {
				t.Fatalf("reported spend=%v summary=%v, want %v (%d harness, %d direct AI calls)", got, result.Summary.CostUSD, want, app.harnessCalls.Load(), app.aiCalls.Load())
			}
			breakdown := result.Metadata.Budget["cost_breakdown"].(map[string]any)
			for _, phase := range []string{"intake", "anatomy", "meta_selectors", "review", "coverage", "synthesis", "output"} {
				if breakdown[phase].(float64) <= 0 {
					t.Errorf("missing paid %s work in breakdown: %v", phase, breakdown)
				}
			}
			if len(result.Review.Comments) == 0 {
				t.Fatal("fixture must exercise paid comment polish")
			}
		})
	}
}

func TestSuppliedSpendStopsLaterReviewWork(t *testing.T) {
	app := &paidReviewApp{}
	cap := .001
	in := schemas.ReviewInput{PrURL: strPtr("https://github.com/o/r/pull/1"), Depth: "standard", MaxCostUSD: &cap}
	cfg, err := config.ReviewConfig{}.FromInput(in)
	if err != nil {
		t.Fatal(err)
	}
	gh := &reviewFixtureGH{}
	o := New(Deps{App: app, GH: gh}, in, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = o.Run(ctx)
	if err == nil || app.harnessCalls.Load() != 0 || len(gh.reviews) != 0 {
		t.Fatalf("cost ceiling ignored: err=%v harness calls=%d publication attempts=%d", err, app.harnessCalls.Load(), len(gh.reviews))
	}
}

func TestReviewCostCeilingOptIn(t *testing.T) {
	cap := 2.0
	for _, tc := range []struct {
		name     string
		env      string
		explicit *float64
		stops    bool
	}{
		{name: "unconfigured"},
		{name: "explicit default value", explicit: &cap, stops: true},
		{name: "environment default value", env: "2.0", stops: true},
		{name: "explicit overrides invalid environment", env: "invalid", explicit: &cap, stops: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetTestEnv(t, "PR_AF_MAX_COST_USD")
			unsetTestEnv(t, "PR_AF_MAX_DURATION_SECONDS")
			if tc.env != "" {
				t.Setenv("PR_AF_MAX_COST_USD", tc.env)
			}
			in := schemas.ReviewInput{PrURL: strPtr("https://github.com/o/r/pull/1"), Depth: "standard", MaxCostUSD: tc.explicit}
			cfg, err := config.ReviewConfig{}.FromInput(in)
			if err != nil {
				t.Fatal(err)
			}
			app := &paidReviewApp{costMultiplier: 10000}
			gh := &reviewFixtureGH{}
			o := New(Deps{App: app, GH: gh}, in, cfg)
			o.buildHaxClientFn = func() *hitl.HaxClient { return nil }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := o.Run(ctx)
			if tc.stops {
				if !errors.Is(err, errBudgetExhausted) || app.harnessCalls.Load() != 0 || len(gh.reviews) != 0 {
					t.Fatalf("configured ceiling did not stop later work: err=%v harness calls=%d publication attempts=%d", err, app.harnessCalls.Load(), len(gh.reviews))
				}
				return
			}
			if err != nil || len(gh.reviews) != 1 {
				t.Fatalf("unconfigured review stopped at supplied spend $%.2f: err=%v publication attempts=%d", o.totalCost(), err, len(gh.reviews))
			}
			want := float64(app.harnessCalls.Load())*10 + float64(app.aiCalls.Load())*20
			got := result.Metadata.Budget["total_cost_usd"].(float64)
			if want <= 2 || got != want || result.Summary.CostUSD != want {
				t.Fatalf("reported spend=%v summary=%v, want %v above $2.00", got, result.Summary.CostUSD, want)
			}
			breakdown := result.Metadata.Budget["cost_breakdown"].(map[string]any)
			var sum float64
			for _, cost := range breakdown {
				sum += cost.(float64)
			}
			if sum != want || breakdown["review"].(float64) == 0 || breakdown["coverage"].(float64) == 0 || breakdown["output"].(float64) == 0 {
				t.Fatalf("review did not complete all paid work: breakdown=%v, want total %v", breakdown, want)
			}
			if len(result.Metadata.Budget) != 5 || result.Metadata.Budget["max_cost_usd"] != 2.0 || result.Metadata.Budget["max_duration_seconds"] != 3600 || result.Metadata.Budget["budget_exhausted"] != false || result.Summary.BudgetExhausted {
				t.Fatalf("default budget metadata changed: %+v", result.Metadata.Budget)
			}
		})
	}
}

func TestUnconfiguredReviewIgnoresPhaseSpendCaps(t *testing.T) {
	unsetTestEnv(t, "PR_AF_MAX_COST_USD")
	unsetTestEnv(t, "PR_AF_MAX_DURATION_SECONDS")
	cfg, err := config.ReviewConfig{}.FromInput(schemas.ReviewInput{})
	if err != nil {
		t.Fatal(err)
	}
	for phase, cap := range cfg.Budget.PhaseBudgets {
		t.Run(phase, func(t *testing.T) {
			o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, cfg)
			o.clock = func() time.Duration { return 10 * time.Second }
			o.registerCost(phase, map[string]any{"cost_usd": 1.0})
			if o.budgetOrTimeoutExhausted(phase) || o.isBudgetExhausted() {
				t.Fatalf("unconfigured phase %s stopped at $1.00 (phase allocation $%.2f, reported ceiling $%.2f)", phase, cap, cfg.Budget.MaxCostUSD)
			}
			o.clock = func() time.Duration { return 4000 * time.Second }
			if !o.budgetOrTimeoutExhausted(phase) || !o.isBudgetExhausted() || o.budgetExhaustedMessage(phase) != "Review time budget exceeded (max_duration_seconds=3600) before "+phase {
				t.Fatal("duration cap must still stop an unconfigured review with supplied spend")
			}
		})
	}
}
