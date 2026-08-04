package node

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/orch"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// pythonSurface is the independent parity checklist: the exact 17 reasoner names
// the Python pr-af node registers, in the design §B.1 canonical order. Written
// from the Python inventory (app.py `review` + reasoners/router), NOT derived
// from RegisterAll, so the test catches drift in either direction.
var pythonSurface = []string{
	"review",
	"intake_phase",
	"anatomy_phase",
	"planning_phase",
	"meta_semantic",
	"meta_mechanical",
	"meta_systemic",
	"review_dimension",
	"compound_finder_phase",
	"post_worthiness_gate",
	"compound_dedup_phase",
	"evidence_verifier",
	"adversary_phase",
	"deepen_findings",
	"extract_obligations",
	"verify_obligation",
	"coverage_gate",
}

// TestRegisterAllExactSurface asserts V1: RegisterAll registers exactly the 17
// §B.1 names, in order, with `review` untagged and the other 16 tagged
// ["review","pr"].
func TestRegisterAllExactSurface(t *testing.T) {
	n := newTestNode(t)
	n.RegisterAll()

	got := n.RegisteredNames()

	// Exact ordered equality — V1 asserts the registration order too.
	if !reflect.DeepEqual(got, pythonSurface) {
		t.Fatalf("registered surface mismatch:\n got  = %v\n want = %v", got, pythonSurface)
	}

	// Duplicate guard: RegisterReasoner dedupes by name, so a duplicate in the
	// recorded slice means two registrations collided on one name.
	seen := map[string]int{}
	for _, name := range got {
		seen[name]++
	}
	for name, c := range seen {
		if c > 1 {
			t.Errorf("reasoner %q registered %d times (collision)", name, c)
		}
	}
	if len(got) != 17 {
		t.Errorf("surface size = %d, want 17", len(got))
	}

	// Tags: review has none; every other reasoner carries exactly ["review","pr"].
	if tags := n.TagsFor("review"); len(tags) != 0 {
		t.Errorf("review tags = %v, want none", tags)
	}
	for _, name := range pythonSurface {
		if name == "review" {
			continue
		}
		if tags := n.TagsFor(name); !reflect.DeepEqual(tags, []string{"review", "pr"}) {
			t.Errorf("%s tags = %v, want [review pr]", name, tags)
		}
	}
}

// TestReviewHandlerErrorMapping asserts the §B.4 error contract at the node
// layer: ErrBadInput -> 400 with the raw message and NO note; any other error ->
// note("Review pipeline failed: <e>", ["review","error"]) then 500 with the
// "review execution failed: " prefix; success returns the result verbatim.
func TestReviewHandlerErrorMapping(t *testing.T) {
	repo := t.TempDir() // existing dir -> ResolveRepo returns it, no clone/network.

	t.Run("bad-input maps to 400 with raw message, no note", func(t *testing.T) {
		fa := &fakeApp{}
		n := &Node{
			NodeID:    "pr-af",
			reviewApp: fa,
			runReview: func(context.Context, orch.Deps, schemas.ReviewInput, config.ReviewConfig) (schemas.ReviewResult, error) {
				return schemas.ReviewResult{}, wrapBadInput("One of pr_url, diff_text, or repo_path is required")
			},
		}

		_, err := n.reviewHandler(context.Background(), map[string]any{"repo_path": repo})
		exec := asExecuteError(t, err)
		if exec.StatusCode != 400 {
			t.Errorf("status = %d, want 400", exec.StatusCode)
		}
		if exec.Message != "One of pr_url, diff_text, or repo_path is required" {
			t.Errorf("message = %q, want the raw ValueError message", exec.Message)
		}
		if len(fa.notes) != 0 {
			t.Errorf("bad-input path must not emit a note, got %v", fa.notes)
		}
	})

	t.Run("other error maps to 500 with prefix + pipeline note", func(t *testing.T) {
		fa := &fakeApp{}
		n := &Node{
			NodeID:    "pr-af",
			reviewApp: fa,
			runReview: func(context.Context, orch.Deps, schemas.ReviewInput, config.ReviewConfig) (schemas.ReviewResult, error) {
				return schemas.ReviewResult{}, errors.New("kaboom")
			},
		}

		_, err := n.reviewHandler(context.Background(), map[string]any{"repo_path": repo})
		exec := asExecuteError(t, err)
		if exec.StatusCode != 500 {
			t.Errorf("status = %d, want 500", exec.StatusCode)
		}
		if exec.Message != "review execution failed: kaboom" {
			t.Errorf("message = %q, want the prefixed message", exec.Message)
		}
		if len(fa.notes) != 1 {
			t.Fatalf("expected exactly one pipeline-failure note, got %v", fa.notes)
		}
		note := fa.notes[0]
		if note.msg != "Review pipeline failed: kaboom" {
			t.Errorf("note message = %q, want %q", note.msg, "Review pipeline failed: kaboom")
		}
		if !reflect.DeepEqual(note.tags, []string{"review", "error"}) {
			t.Errorf("note tags = %v, want [review error]", note.tags)
		}
	})

	t.Run("success returns the result verbatim", func(t *testing.T) {
		fa := &fakeApp{}
		want := schemas.ReviewResult{ReviewID: "rev_abc123", PrURL: "https://example/pr/1"}
		n := &Node{
			NodeID:    "pr-af",
			reviewApp: fa,
			runReview: func(context.Context, orch.Deps, schemas.ReviewInput, config.ReviewConfig) (schemas.ReviewResult, error) {
				return want, nil
			},
		}

		out, err := n.reviewHandler(context.Background(), map[string]any{"repo_path": repo})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, ok := out.(schemas.ReviewResult)
		if !ok {
			t.Fatalf("result type = %T, want schemas.ReviewResult", out)
		}
		if got.ReviewID != want.ReviewID || got.PrURL != want.PrURL {
			t.Errorf("result = %+v, want %+v", got, want)
		}
		if len(fa.notes) != 0 {
			t.Errorf("success path must not emit a note, got %v", fa.notes)
		}
	})
}

// TestReviewHandlerClampsDepthAndResolvesRepo proves the bind-layer coercions
// reach the orchestrator: max_review_depth is clamped to 3 and the empty
// repo_path is filled by ResolveRepo before the pipeline runs.
func TestReviewHandlerClampsDepthAndResolvesRepo(t *testing.T) {
	t.Setenv("PR_AF_REPO_PATH", t.TempDir()) // ResolveRepo fallback when repo_path is empty.

	var seenInput schemas.ReviewInput
	fa := &fakeApp{}
	n := &Node{
		NodeID:    "pr-af",
		reviewApp: fa,
		runReview: func(_ context.Context, _ orch.Deps, in schemas.ReviewInput, _ config.ReviewConfig) (schemas.ReviewResult, error) {
			seenInput = in
			return schemas.ReviewResult{}, nil
		},
	}

	if _, err := n.reviewHandler(context.Background(), map[string]any{
		"diff_text":        "diff --git a b",
		"max_review_depth": 9,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if seenInput.MaxReviewDepth != 3 {
		t.Errorf("max_review_depth = %d, want clamped to 3", seenInput.MaxReviewDepth)
	}
	if seenInput.RepoPath == nil || *seenInput.RepoPath == "" {
		t.Errorf("repo_path was not resolved: %v", seenInput.RepoPath)
	}
}

// --- helpers -----------------------------------------------------------------

// newTestNode builds a node from a cleared env so BuildAgent is deterministic
// (no ambient OPENROUTER_API_KEY forcing AIConfig, node id/port at defaults).
func newTestNode(t *testing.T) *Node {
	t.Helper()
	t.Setenv("NODE_ID", "")
	t.Setenv("PORT", "")
	t.Setenv("AGENTFIELD_SERVER", "")
	t.Setenv("AGENT_CALLBACK_URL", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	n, err := BuildAgent("pr-af", "8007", "AI-Native Pull Request Review Agent")
	if err != nil {
		t.Fatalf("BuildAgent: %v", err)
	}
	return n
}

func asExecuteError(t *testing.T, err error) *agent.ExecuteError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var exec *agent.ExecuteError
	if !errors.As(err, &exec) {
		t.Fatalf("error is not *agent.ExecuteError: %T (%v)", err, err)
	}
	return exec
}

// wrapBadInput builds an error that errors.Is(err, orch.ErrBadInput) while
// reporting only the raw message — the shape orch's badInputError produces, so
// the node's 400 body is byte-identical to Python's str(ValueError).
type badInputWrap struct{ msg string }

func (e badInputWrap) Error() string { return e.msg }
func (e badInputWrap) Unwrap() error { return orch.ErrBadInput }

func wrapBadInput(msg string) error { return badInputWrap{msg: msg} }

// fakeApp is a minimal orch.App used by the error-mapping tests. Only Note is
// exercised (runReview is stubbed); Harness/AI/Pause satisfy the interface.
type fakeApp struct {
	notes []recordedNote
}

type recordedNote struct {
	msg  string
	tags []string
}

func (f *fakeApp) Harness(context.Context, string, map[string]any, any, harness.Options) (*harness.Result, error) {
	return nil, nil
}

func (f *fakeApp) AI(context.Context, string, ...ai.Option) (*ai.Response, error) {
	return nil, nil
}

func (f *fakeApp) Pause(context.Context, agent.PauseOptions) (*agent.ApprovalResult, error) {
	return nil, nil
}

func (f *fakeApp) Note(_ context.Context, message string, tags ...string) {
	f.notes = append(f.notes, recordedNote{msg: message, tags: append([]string(nil), tags...)})
}

// compile-time proof the fake satisfies the orchestrator surface.
var _ orch.App = (*fakeApp)(nil)
