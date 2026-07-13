// Package gates ports the two parallel `.ai()` post-processing passes from the
// Python PR-AF pipeline: the merge-blocker gate (merge_gate.py) and the comment
// polish pass (polish.py). Both fan out one AI call per item, preserve input
// order, and degrade gracefully on per-item failure.
//
// Merge gate — a separate lens from severity. Severity asks "how bad is this
// issue?"; the merge gate asks "must this be fixed BEFORE the PR ships, or can
// it ship and be addressed in a follow-up?". Failure mode: default to
// blocking=false on every error path, because a false negative (a real blocker
// slips through as advisory) is recoverable by human review, whereas a false
// positive (advisory issue flagged blocking) erodes trust and blocks merge.
package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/agentfield/sdk/go/ai"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// AICaller is the minimal one-method seam the two `.ai()` gates drive. It
// mirrors the AgentField Go SDK Agent.AI (sdk/go/agent/agent.go), so the live
// *agent.Agent satisfies it unchanged and tests supply a fake. This is the Go
// analogue of Python's `app.ai(prompt, system=..., response_format="json")`:
// the system prompt is passed as ai.WithSystem and JSON mode as ai.WithJSONMode.
type AICaller interface {
	AI(ctx context.Context, prompt string, opts ...ai.Option) (*ai.Response, error)
}

// Compile-time proof the real SDK agent is an AICaller, so the orchestrator can
// hand the live *agent.Agent to these gates unchanged.
var _ AICaller = (*agent.Agent)(nil)

// mergeGateVerdict ports merge_gate.MergeGateVerdict: the per-finding gate
// output. blocking defaults false; reason is capped at 400 chars.
type mergeGateVerdict struct {
	blocking bool
	reason   string
}

// responseText extracts the text body from an AI response — the Go analogue of
// Python's `getattr(out, "text", None) or getattr(out, "content", None) or
// str(out)`. The Go *ai.Response exposes a single Text() accessor, so the
// Python `.content`/`str(out)` fallbacks (which key off the Python SDK object's
// alternate attributes) collapse to this one call. A nil response yields "".
func responseText(out *ai.Response) string {
	if out == nil {
		return ""
	}
	return out.Text()
}

// parseVerdict ports merge_gate._parse_verdict: tolerant JSON parsing that
// strips markdown fences, extracts the first `{...}` block from surrounding
// prose, and falls back to a non-blocking advisory verdict on any malformed
// input. The three fallback reason strings ("gate parse error",
// "gate non-object", "gate error") are load-bearing parity contracts.
func parseVerdict(raw string) mergeGateVerdict {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```") {
		text = strings.Trim(text, "`")
		if strings.HasPrefix(text, "json") {
			text = strings.TrimSpace(text[4:])
		}
	}
	// Find first `{...}` block (Python: text.find("{") / text.rfind("}")).
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}
	var data any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return mergeGateVerdict{blocking: false, reason: "gate parse error"}
	}
	obj, ok := data.(map[string]any)
	if !ok {
		// Valid JSON that is not an object (array, number, string, bool, null).
		return mergeGateVerdict{blocking: false, reason: "gate non-object"}
	}
	return mergeGateVerdict{
		// Python: bool(data.get("blocking", False)) — apply Python truthiness.
		blocking: pyTruthy(obj["blocking"]),
		// Python: str(data.get("reason", ""))[:400] — code-point truncation.
		reason: truncateRunes(reasonStr(obj), 400),
	}
}

// pyTruthy mirrors Python's bool() coercion over the value types that
// encoding/json produces (nil, bool, float64, string, []any, map[string]any):
// zero/empty is false, everything else is true.
func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return false
	}
}

// reasonStr reproduces Python's `str(data.get("reason", ""))`: an absent key
// yields the default "" (NOT "None"), whereas a present JSON null yields
// str(None) == "None".
func reasonStr(obj map[string]any) string {
	v, ok := obj["reason"]
	if !ok {
		return ""
	}
	return pyStr(v)
}

// pyStr approximates Python's str() over JSON scalars. Strings pass through;
// null/bool/number match CPython's str() for the realistic cases. Whole floats
// render without a decimal point (matching str(int)); containers fall back to a
// JSON rendering. (Divergence: CPython's int-vs-float distinction for numeric
// reasons cannot be recovered because encoding/json decodes every JSON number
// to float64 — an unreachable edge since reasons are always strings.)
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", t)
	}
}

// truncateRunes mirrors Python's str[:n] — truncation by code point, not byte.
func truncateRunes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// gateOne ports merge_gate._gate_one: one AI call for a single finding. Any AI
// error is swallowed and mapped to the non-blocking "gate error" verdict so a
// gate outage never blocks merge (mirrors Python's try/except returning
// MergeGateVerdict(blocking=False, reason="gate error")).
func gateOne(ctx context.Context, app AICaller, f schemas.ScoredFinding) mergeGateVerdict {
	// response_format=json (WithJSONMode) forces JSON output so reasoning models
	// don't leak chain-of-thought before the verdict and truncate the answer.
	out, err := app.AI(ctx, prompts.MergeGateUserPrompt(f),
		ai.WithSystem(prompts.MergeGateSystem),
		ai.WithJSONMode(),
	)
	if err != nil {
		fmt.Printf("[PR-AF] Merge-gate skipped for %s: %T\n", f.ID, err)
		return mergeGateVerdict{blocking: false, reason: "gate error"}
	}
	return parseVerdict(responseText(out))
}

// ClassifyFindings ports merge_gate.classify_findings: one AI call per finding,
// fired in parallel, original order preserved. Returns a NEW slice with
// Blocking and BlockingReason populated; the input slice and its elements are
// not mutated (pure, mirroring pydantic model_copy).
//
// Concurrency: Python uses asyncio.gather WITHOUT return_exceptions, but
// _gate_one catches every exception internally and never raises, so no error
// ever propagates through gather. The faithful Go mirror is a WaitGroup writing
// into a pre-indexed results slice — order is positional, not completion-order.
func ClassifyFindings(ctx context.Context, app AICaller, findings []schemas.ScoredFinding) []schemas.ScoredFinding {
	if len(findings) == 0 {
		return findings
	}
	verdicts := make([]mergeGateVerdict, len(findings))
	var wg sync.WaitGroup
	for i := range findings {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			verdicts[i] = gateOne(ctx, app, findings[i])
		}(i)
	}
	wg.Wait()

	classified := make([]schemas.ScoredFinding, len(findings))
	blockingCount := 0
	for i, f := range findings {
		nf := f // shallow struct copy — sets only the two scalar gate fields.
		nf.Blocking = verdicts[i].blocking
		nf.BlockingReason = verdicts[i].reason
		classified[i] = nf
		if nf.Blocking {
			blockingCount++
		}
	}
	fmt.Printf("[PR-AF] Merge-gate: %d/%d findings classified blocking\n", blockingCount, len(classified))
	return classified
}
