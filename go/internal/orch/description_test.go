package orch

import (
	"context"
	"strings"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func TestParallelReviewPassesCappedPRDescription(t *testing.T) {
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())
	description := strings.Repeat("a", 3990) + "IN_RANGE" + strings.Repeat("b", 1000)
	o.prData = &schemas.GitHubPRData{Description: description}

	gotDescription := ""
	o.rfns.reviewDim = func(_ context.Context, _ reasoners.Deps, in reasoners.ReviewDimensionInput) (map[string]any, error) {
		gotDescription = in.PrDescription
		return map[string]any{
			"findings":            []any{},
			"sub_reviews":         []any{},
			"schema_parse_failed": false,
		}, nil
	}
	plan := schemas.ReviewPlan{Dimensions: []schemas.ReviewDimension{{
		ID: "d1", Name: "Correctness", ReviewPrompt: "Review it.", TargetFiles: []string{"a.go"},
	}}}
	findings := make(chan []schemas.ReviewFinding, 1)
	if err := o.runParallelReview(context.Background(), plan, findings, 0, "", &dimensionParseStats{}); err != nil {
		t.Fatal(err)
	}

	want := string([]rune(description)[:4000])
	if gotDescription != want {
		t.Fatalf("review input description has %d runes, want capped content with %d", len([]rune(gotDescription)), len([]rune(want)))
	}
}
