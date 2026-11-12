package orch

import (
	"context"
	"encoding/json"
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
	cfg := config.DefaultReviewConfig()
	cfg.Budget.MaxCostUSD = .001
	gh := &reviewFixtureGH{}
	o := New(Deps{App: app, GH: gh}, schemas.ReviewInput{PrURL: strPtr("https://github.com/o/r/pull/1"), Depth: "standard"}, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := o.Run(ctx)
	if err == nil || app.harnessCalls.Load() != 0 || len(gh.reviews) != 0 {
		t.Fatalf("cost ceiling ignored: err=%v harness calls=%d publication attempts=%d", err, app.harnessCalls.Load(), len(gh.reviews))
	}
}
