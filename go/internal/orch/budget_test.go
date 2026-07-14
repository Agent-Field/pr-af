package orch

// V7 (orch half) + error taxonomy (item 6): the cost counter stays inert (reasoner
// returns never carry cost_usd), only the wall-clock gate trips, and the
// no-source guard surfaces ErrBadInput with the verbatim message the node maps to
// HTTP 400.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func TestCostStaysInert(t *testing.T) {
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())
	// A realistic reasoner return: no cost_usd key anywhere.
	o.registerCost("intake", map[string]any{"pr_type": "feature", "findings": []any{}})
	o.registerCost("review", nil)
	if got := o.totalCost(); got != 0.0 {
		t.Errorf("total cost = %v, want 0.0 (inert)", got)
	}
	// The mechanism still works when a cost IS present (defensive coverage).
	o.registerCost("intake", map[string]any{"cost_usd": 0.25})
	if got := o.totalCost(); got != 0.25 {
		t.Errorf("total cost after explicit cost_usd = %v, want 0.25", got)
	}
}

func TestWallClockBudgetTrips(t *testing.T) {
	// Production builds config via FromInput, which resolves the effective
	// duration cap to 3600s (ResolveBudgetCaps default).
	cfg, cfgErr := config.ReviewConfig{}.FromInput(schemas.ReviewInput{})
	if cfgErr != nil {
		t.Fatalf("FromInput: %v", cfgErr)
	}
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, cfg)
	o.clock = func() time.Duration { return 10 * time.Second }
	if o.budgetOrTimeoutExhausted("review") {
		t.Error("should not be exhausted at 10s")
	}
	if o.isBudgetExhausted() {
		t.Error("budgetExhausted flag set prematurely")
	}
	o.clock = func() time.Duration { return 4000 * time.Second }
	if !o.budgetOrTimeoutExhausted("intake") {
		t.Error("should be exhausted past the 3600s wall-clock cap")
	}
	if !o.isBudgetExhausted() {
		t.Error("budgetExhausted flag not set after timeout")
	}
	// The wall-clock cap must be reported as a time budget, not a cost budget
	// (§B.4 string — byte-identical to Python's _budget_exhausted_message).
	want := "Review time budget exceeded (max_duration_seconds=3600) before intake"
	if got := o.budgetExhaustedMessage("intake"); got != want {
		t.Errorf("duration-cap message = %q, want %q", got, want)
	}
}

func TestCostCapKeepsBudgetExhaustedWording(t *testing.T) {
	// When the COST cap trips (duration cap untouched), the historical
	// "Budget exhausted before <phase>" wording is preserved (§B.4).
	cfg, cfgErr := config.ReviewConfig{}.FromInput(schemas.ReviewInput{})
	if cfgErr != nil {
		t.Fatalf("FromInput: %v", cfgErr)
	}
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, cfg)
	o.clock = func() time.Duration { return 10 * time.Second }
	o.registerCost("review", map[string]any{"cost_usd": cfg.Budget.MaxCostUSD})
	if !o.budgetOrTimeoutExhausted("review") {
		t.Fatal("should be exhausted at the cost cap")
	}
	want := "Budget exhausted before review"
	if got := o.budgetExhaustedMessage("review"); got != want {
		t.Errorf("cost-cap message = %q, want %q", got, want)
	}
}

func TestPhaseCapNeverTripsWithZeroCost(t *testing.T) {
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())
	o.clock = func() time.Duration { return 0 }
	// Every positive-cap phase: spent (0) < cap → false.
	for _, phase := range []string{"intake", "anatomy", "meta_selectors", "review", "adversary", "cross_ref", "coverage"} {
		if o.budgetOrTimeoutExhausted(phase) {
			t.Errorf("phase %q tripped with zero cost", phase)
		}
	}
}

func TestRunNoSourceIsBadInput(t *testing.T) {
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{Depth: "auto"}, config.DefaultReviewConfig())
	_, err := o.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error for a review with no source")
	}
	if !errors.Is(err, ErrBadInput) {
		t.Errorf("error should wrap ErrBadInput (→ HTTP 400), got %v", err)
	}
	if err.Error() != "One of pr_url, diff_text, or repo_path is required" {
		t.Errorf("error message = %q, want the verbatim ValueError string", err.Error())
	}
}
