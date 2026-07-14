package node

// DAG wiring: the review handler must hand the node's tracked local-call seam
// to the orchestrator (orch.Deps.Local), and BuildAgent must point that seam
// at the live SDK agent — that is the link that turns the in-process pipeline
// phases into control-plane child executions (the review DAG).

import (
	"context"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/orch"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// recordingLocal is a sentinel orch.LocalCaller.
type recordingLocal struct{}

func (recordingLocal) CallLocal(context.Context, string, map[string]any) (any, error) {
	return map[string]any{}, nil
}

func TestReviewHandlerForwardsLocalCaller(t *testing.T) {
	repo := t.TempDir() // existing dir -> ResolveRepo returns it, no clone/network.
	sentinel := recordingLocal{}

	var got orch.Deps
	n := &Node{
		NodeID:      "pr-af-go",
		reviewApp:   &fakeApp{},
		localCaller: sentinel,
		runReview: func(_ context.Context, deps orch.Deps, _ schemas.ReviewInput, _ config.ReviewConfig) (schemas.ReviewResult, error) {
			got = deps
			return schemas.ReviewResult{}, nil
		},
	}

	if _, err := n.reviewHandler(context.Background(), map[string]any{"repo_path": repo}); err != nil {
		t.Fatalf("reviewHandler: %v", err)
	}
	if got.Local != orch.LocalCaller(sentinel) {
		t.Fatalf("orch.Deps.Local = %#v, want the node's localCaller seam", got.Local)
	}
}

func TestBuildAgentWiresLocalCallerToApp(t *testing.T) {
	t.Setenv("NODE_ID", "")
	t.Setenv("AGENTFIELD_SERVER", "")
	t.Setenv("AGENTFIELD_API_KEY", "")
	t.Setenv("PORT", "")
	t.Setenv("OPENROUTER_API_KEY", "")

	n, err := BuildAgent("pr-af-go", "8007", "desc")
	if err != nil {
		t.Fatalf("BuildAgent: %v", err)
	}
	if n.localCaller != orch.LocalCaller(n.App) {
		t.Fatal("BuildAgent must wire localCaller to the live agent (App)")
	}
}
