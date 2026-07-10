package node

// register.go wires the exact 17-reasoner surface (design §B.1) onto the agent.
// The single externally-driven reasoner is `review` (no tags, carries the §B.1
// input schema); the other 16 are the router reasoners the orchestrator invokes
// in-process but that Python still CP-registers, each tagged ["review","pr"].
//
// Those 16 tags are SEMANTIC domain tags, not node-identity tags — node identity
// is carried by node_id=pr-af-go, so callers reach pr-af-go.review. They are NOT
// renamed to the node id (unlike SWE-AF's -go role tags).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Agent-Field/agentfield/sdk/go/agent"

	"github.com/Agent-Field/pr-af/go/internal/afx"
	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/orch"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// reviewTags is the shared domain tag set for the 16 internal reasoners.
var reviewTags = []string{"review", "pr"}

// RegisterAll registers the full PR-AF surface: `review` (externally driven) +
// the 16 in-process router reasoners. Registration order matches §B.1 so the
// recorded slice reads as the design's canonical list.
func (n *Node) RegisterAll() {
	// review — the only externally-driven reasoner. No tags; carries the §B.1
	// input schema so the CP UI card shows the real parameters. Error mapping is
	// applied inside reviewHandler (ErrBadInput -> 400; else note + 500 prefix).
	n.record("review", nil)
	n.App.RegisterReasoner("review", n.reviewHandler, agent.WithInputSchema(reviewInputSchema))

	// The 16 router reasoners, tagged ["review","pr"], in §B.1 order. Each is
	// bound (afx.Bind) into its typed input and backed by a reasoners.Deps built
	// from the agent (Harness=App, AI=App).
	regReasoner(n, "intake_phase", reasoners.IntakePhase)
	regReasoner(n, "anatomy_phase", reasoners.AnatomyPhase)
	regReasoner(n, "planning_phase", reasoners.PlanningPhase)
	regReasoner(n, "meta_semantic", reasoners.MetaSemantic)
	regReasoner(n, "meta_mechanical", reasoners.MetaMechanical)
	regReasoner(n, "meta_systemic", reasoners.MetaSystemic)
	regReasoner(n, "review_dimension", reasoners.ReviewDimension)
	regReasoner(n, "compound_finder_phase", reasoners.CompoundFinderPhase)
	regReasoner(n, "post_worthiness_gate", reasoners.PostWorthinessGate)
	regReasoner(n, "compound_dedup_phase", reasoners.CompoundDedupPhase)
	regReasoner(n, "evidence_verifier", reasoners.EvidenceVerifier)
	regReasoner(n, "adversary_phase", reasoners.AdversaryPhase)
	regReasoner(n, "deepen_findings", reasoners.DeepenFindings)
	regReasoner(n, "extract_obligations", reasoners.ExtractObligations)
	regReasoner(n, "verify_obligation", reasoners.VerifyObligation)
	regReasoner(n, "coverage_gate", reasoners.CoverageGate)
}

// record appends name (and its tags) to the node's registration bookkeeping —
// the source of truth for the parity test. tags==nil records an empty slice so
// TagsFor("review") returns no tags.
func (n *Node) record(name string, tags []string) {
	n.registered = append(n.registered, name)
	n.tags[name] = append([]string(nil), tags...)
}

// regReasoner registers one internal router reasoner under name with the
// ["review","pr"] tags. It adapts a typed reasoner func
// (func(ctx, reasoners.Deps, T) (map[string]any, error)) to the SDK HandlerFunc
// by afx.Bind-ing the request map into T. T is inferred from fn. The recorded
// tags and the registered tags come from the same reviewTags slice, so the
// parity bookkeeping cannot drift from what the CP receives.
func regReasoner[T any](
	n *Node,
	name string,
	fn func(context.Context, reasoners.Deps, T) (map[string]any, error),
) {
	n.record(name, reviewTags)
	deps := reasoners.Deps{Harness: n.App, AI: n.App}
	n.App.RegisterReasoner(name, func(ctx context.Context, input map[string]any) (any, error) {
		in, err := afx.Bind[T](input)
		if err != nil {
			return nil, err
		}
		return fn(ctx, deps, in)
	}, agent.WithReasonerTags(reviewTags...))
}

// reviewHandler ports app.py review(): bind the request into a ReviewInput
// (afx.Bind runs the struct's default-seeding UnmarshalJSON), clamp
// max_review_depth to 3 at the bind layer, resolve the repo path, build the
// per-call config, then run the orchestrator through the runReview seam with the
// §B.4 error mapping.
func (n *Node) reviewHandler(ctx context.Context, input map[string]any) (any, error) {
	in, err := afx.Bind[schemas.ReviewInput](input)
	if err != nil {
		// A malformed body is a client error (Python: pydantic validation -> 422;
		// mapped here to 400 as the closest node-level bad-input signal).
		return nil, &agent.ExecuteError{StatusCode: http.StatusBadRequest, Message: err.Error()}
	}

	// Bind-layer clamp — min(max_review_depth, 3). Python clamps here AND again in
	// ReviewConfig.FromInput (config.go); both are reproduced.
	in.MaxReviewDepth = min(in.MaxReviewDepth, 3)

	// Resolve the repo path (app.py:231, called OUTSIDE the mapped try). Empty
	// strings stand in for Python's None.
	resolved, err := orch.ResolveRepo(ctx, strp(in.RepoPath), strp(in.PrURL))
	if err != nil {
		// Python leaves a _resolve_repo ValueError uncaught, so FastAPI returns a
		// generic 500 (message hidden). Go surfaces the clone/checkout message at
		// 500 — structurally the same status, more debuggable. No pipeline note
		// and no "review execution failed:" prefix (that path is the orchestrator's).
		return nil, &agent.ExecuteError{StatusCode: http.StatusInternalServerError, Message: err.Error()}
	}
	if strp(in.RepoPath) == "" {
		in.RepoPath = &resolved
	}

	cfg := config.ReviewConfig{}.FromInput(in)

	deps := orch.Deps{
		App:              n.reviewApp,
		GH:               n.gh,
		NodeID:           n.NodeID,
		AgentFieldServer: n.AgentFieldServer,
	}

	result, err := n.runReview(ctx, deps, in, cfg)
	if err != nil {
		if errors.Is(err, orch.ErrBadInput) {
			// ValueError-class -> 400 with the RAW message (badInputError.Error()
			// reports only the message, so the body is byte-identical to Python's
			// str(ValueError)).
			return nil, &agent.ExecuteError{StatusCode: http.StatusBadRequest, Message: err.Error()}
		}
		// Any other failure: emit the pipeline-failure note (tags ["review","error"])
		// then 500 with the "review execution failed: " prefix — exactly app.py:243-245.
		deps.App.Note(ctx, "Review pipeline failed: "+err.Error(), "review", "error")
		return nil, &agent.ExecuteError{
			StatusCode: http.StatusInternalServerError,
			Message:    "review execution failed: " + err.Error(),
		}
	}

	// Python returns result.model_dump(); the ReviewResult struct marshals to the
	// identical snake_case key set (no omitempty), so returning it directly yields
	// the same JSON the async status callback / sync response carries.
	return result, nil
}

// strp dereferences a *string (nil -> "").
func strp(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// reviewInputSchema is the §B.1 input schema for `review`, transcribed from the
// app.py review() signature (exact param names/types/defaults). additionalProperties
// stays true so the async API body remains byte-compatible with Python (extra
// keys accepted). Nullable params are typed by their non-null base type — the
// schema is a UI-display aid, and the permissive additionalProperties keeps
// binding lossless.
var reviewInputSchema = json.RawMessage(`{"type":"object","additionalProperties":true,"properties":{` +
	`"pr_url":{"type":"string"},"diff_text":{"type":"string"},"repo_path":{"type":"string"},` +
	`"base_ref":{"type":"string"},"head_ref":{"type":"string"},"depth":{"type":"string","default":"auto"},` +
	`"max_cost_usd":{"type":"number"},"max_duration_seconds":{"type":"integer"},"focus":{"type":"string","default":"auto"},` +
	`"ignore_paths":{"type":"array","items":{"type":"string"}},"hints":{"type":"array","items":{"type":"string"}},` +
	`"models":{"type":"object"},"max_concurrent_reviewers":{"type":"integer"},"max_coverage_iterations":{"type":"integer"},` +
	`"max_review_depth":{"type":"integer","default":2},"output_format":{"type":"string","default":"github"},` +
	`"dry_run":{"type":"boolean","default":false},"post_pr_number":{"type":"integer"},` +
	`"suggestion_mode":{"type":"string","default":"comment"}}}`)
