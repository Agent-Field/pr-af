package hitl

import (
	"context"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
)

// Pauser is the pause surface the PR-AF review gate drives. The AgentField Go
// SDK *agent.Agent satisfies it via its Pause method; tests supply a fake.
//
// Pause transitions the execution to "waiting" on the control plane and blocks
// until the approval webhook callback resolves it (or it expires) — the direct
// port of Python's app.pause() used by review_gate.py::request_review_approval.
// There is no polling: the SDK registers the pending pause and the agent's
// /webhooks/approval route resolves it when the human responds.
type Pauser interface {
	Pause(ctx context.Context, opts agent.PauseOptions) (*agent.ApprovalResult, error)
}

// Compile-time proof that the real SDK agent is a Pauser, so the review gate
// can accept the seam and be handed the live *agent.Agent unchanged.
var _ Pauser = (*agent.Agent)(nil)

// App is the minimal slice of *agent.Agent the HITL primitives need: the
// fire-and-forget note channel. Kept as an interface so tests can supply a
// silent stub (mirrors the Python tests that mock app.note).
type App interface {
	Note(ctx context.Context, message string, tags ...string)
}

// noteSafe fires a note when app is non-nil; a nil app is a no-op so the
// primitives stay usable in tests / contexts without an agent.
func noteSafe(ctx context.Context, app App, message string, tags ...string) {
	if app != nil {
		app.Note(ctx, message, tags...)
	}
}

// extractValuesFromRaw finds the submitted form/template values inside an
// approval response payload. Ports pr_af/hitl/client.py::extract_values_from_raw:
// prefer raw["values"], then raw["response"]["values"]; anything else -> {}.
func extractValuesFromRaw(raw map[string]any) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	if direct, ok := raw["values"].(map[string]any); ok {
		return copyAnyMap(direct)
	}
	if respObj, ok := raw["response"].(map[string]any); ok {
		if inner, ok := respObj["values"].(map[string]any); ok {
			return copyAnyMap(inner)
		}
	}
	return map[string]any{}
}

func copyAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
