package reasoners

import (
	"context"
	"math"
	"testing"

	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"
)

type paidHarness struct {
	mockHarness
	cost float64
}

func (h *paidHarness) Harness(ctx context.Context, prompt string, schema map[string]any, dest any, opts harness.Options) (*harness.Result, error) {
	res, err := h.mockHarness.Harness(ctx, prompt, schema, dest, opts)
	if res != nil {
		res.CostUSD = &h.cost
	}
	return res, err
}

type paidAI struct {
	fakeAISeq
	cost float64
}

func (a *paidAI) AI(ctx context.Context, prompt string, opts ...ai.Option) (*ai.Response, error) {
	res, err := a.fakeAISeq.AI(ctx, prompt, opts...)
	if res != nil {
		res.Usage = &ai.Usage{Cost: &a.cost}
	}
	return res, err
}

func TestPhaseCostsIncludePaidAttemptsAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		run       func(Deps) (map[string]any, error)
		texts     []string
		want      float64
		parseFail bool
	}{
		{"selector", func(d Deps) (map[string]any, error) { return MetaSemantic(context.Background(), d, MetaInput{}) }, nil, .03, false},
		{"selector fallback", func(d Deps) (map[string]any, error) { return MetaSemantic(context.Background(), d, MetaInput{}) }, nil, .03, true},
		{"intake retry and fallback", func(d Deps) (map[string]any, error) { return IntakePhase(context.Background(), d, IntakeInput{}) }, []string{"malformed", `{"confident":false}`}, .07, false},
		{"coverage retries and fallback", func(d Deps) (map[string]any, error) {
			return CoverageGate(context.Background(), d, CoverageGateInput{})
		}, []string{"malformed"}, .09, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &paidHarness{mockHarness: mockHarness{payload: `{}`, parseFail: tc.parseFail}, cost: .03}
			a := &paidAI{fakeAISeq: fakeAISeq{texts: tc.texts}, cost: .02}
			out, err := tc.run(Deps{Harness: h, AI: a})
			if err != nil {
				t.Fatal(err)
			}
			cost, ok := out["cost_usd"].(float64)
			if !ok || math.Abs(cost-tc.want) > 1e-9 {
				t.Fatalf("phase cost=%v, want %v from paid calls; output=%v", out["cost_usd"], tc.want, out)
			}
		})
	}
}
