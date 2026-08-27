package orch

// Streaming-path parity (design risk 1): the gate-OFF default runs the reviewers
// and the layer CONCURRENTLY through one channel — the layer must consume as
// reviewers complete, not after all of them finish. Order-preservation parity
// (risk 2): gather-based fan-outs (meta-selectors) write into a pre-indexed
// slice, so the output order matches the input order regardless of the order
// goroutines actually complete in.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

func TestStreamingLayerConsumesWhileReviewersRun(t *testing.T) {
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())

	// The third reviewer blocks until the layer signals it has consumed a batch
	// from one of the first two — provable only if the layer streams. A
	// gather-then-process layer would never consume until every reviewer finished,
	// but the third reviewer waits on that consumption → deadlock → test timeout.
	release := make(chan struct{})
	var once sync.Once
	o.rfns.reviewDim = func(_ context.Context, _ reasoners.Deps, in reasoners.ReviewDimensionInput) (map[string]any, error) {
		if in.ReviewPrompt == "block" {
			<-release
		}
		return map[string]any{
			"findings": []any{map[string]any{
				"title": "t-" + in.ReviewPrompt, "file_path": in.ReviewPrompt + ".py",
				"line_start": 1, "severity": "suggestion",
			}},
			"sub_reviews":   []any{},
			"current_depth": 0,
		}, nil
	}
	o.rfns.adversary = func(_ context.Context, _ reasoners.Deps, in reasoners.AdversaryInput) (map[string]any, error) {
		results := make([]any, len(in.Findings))
		for i, finding := range in.Findings {
			results[i] = map[string]any{
				"finding_title": finding.Title,
				"verdict":       "confirmed",
				"reason":        "verified in streaming test",
			}
		}
		return map[string]any{"results": results}, nil
	}
	o.layerBatchHook = func([]schemas.ReviewFinding) {
		once.Do(func() { close(release) })
	}

	plan := schemas.ReviewPlan{Dimensions: []schemas.ReviewDimension{
		{ID: "d0", Name: "D0", ReviewPrompt: "a", TargetFiles: []string{"a.py"}},
		{ID: "d1", Name: "D1", ReviewPrompt: "b", TargetFiles: []string{"b.py"}},
		{ID: "d2", Name: "D2", ReviewPrompt: "block", TargetFiles: []string{"c.py"}},
	}}

	done := make(chan error, 1)
	go func() {
		_, _, err := o.streamReviewLayer(context.Background(), plan, schemas.AnatomyResult{}, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("streamReviewLayer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: layer did not consume until all reviewers finished (not streaming)")
	}
}

func metaResultMap(lens, dimID, target string) map[string]any {
	return map[string]any{
		"lens": lens,
		"dimensions": []any{map[string]any{
			"id": dimID, "name": "N-" + dimID, "target_files": []any{target}, "priority": 1,
		}},
		"confidence": 0.9,
		"rationale":  "",
	}
}

func TestMetaSelectorOrderPreservedUnderAdversarialScheduling(t *testing.T) {
	for iter := 0; iter < 8; iter++ {
		o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())

		// Force out-of-order completion: systemic finishes first, semantic last.
		o.rfns.metaSemantic = func(context.Context, reasoners.Deps, reasoners.MetaInput) (map[string]any, error) {
			time.Sleep(12 * time.Millisecond)
			return metaResultMap("semantic", "a", "fa"), nil
		}
		o.rfns.metaMechanical = func(context.Context, reasoners.Deps, reasoners.MetaInput) (map[string]any, error) {
			time.Sleep(6 * time.Millisecond)
			return metaResultMap("mechanical", "b", "fb"), nil
		}
		o.rfns.metaSystemic = func(context.Context, reasoners.Deps, reasoners.MetaInput) (map[string]any, error) {
			return metaResultMap("systemic", "c", "fc"), nil
		}

		plan, err := o.runMetaSelectors(context.Background(), schemas.IntakeResult{}, schemas.AnatomyResult{}, "standard", "")
		if err != nil {
			t.Fatalf("iter %d: runMetaSelectors: %v", iter, err)
		}
		want := []string{"semantic_a", "mechanical_b", "systemic_c"}
		if len(plan.Dimensions) != len(want) {
			t.Fatalf("iter %d: got %d dimensions, want %d", iter, len(plan.Dimensions), len(want))
		}
		for i, w := range want {
			if plan.Dimensions[i].ID != w {
				t.Fatalf("iter %d: dimension[%d].ID = %q, want %q (fan-out order not preserved)",
					iter, i, plan.Dimensions[i].ID, w)
			}
		}
	}
}
