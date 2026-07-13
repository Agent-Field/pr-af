// Package reasoners ports the 16 router reasoners in
// src/pr_af/reasoners/harnesses.py (intake_phase … coverage_gate) to Go.
//
// Each reasoner is a plain function
//
//	func XxxPhase(ctx context.Context, deps Deps, in XxxInput) (map[string]any, error)
//
// that builds its prompt with the byte-verbatim builders in internal/prompts,
// invokes the harness through the harnessx.Run choke point (or the AI seam for
// the two .ai() gates: IntakePhase and CoverageGate), and returns the exact
// snake_case key set Python's model_dump() emits (design §B.2). The
// orchestrator (T4.1) calls these in-process; node/register.go (T4.2) wraps
// them in afx.Bind adapters for control-plane registration.
//
// Parse-failure behavior mirrors Python per reasoner: when the harness could
// not produce a schema-valid result (Result.Parsed == nil), each reasoner
// applies the same deterministic fallback its Python counterpart does — a
// seeded default struct, an empty key set, or a keep-everything index list —
// never an error (design §C.3 step 4). Harness transport errors and fatal
// (non-retryable) API errors propagate as Go errors, exactly as Python lets
// the exception escape the reasoner.
package reasoners

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/agentfield/sdk/go/ai"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
)

// AICaller is the seam for the SDK's LLM chat entry point (Agent.AI), used by
// the two .ai() reasoners (intake_phase's gate and coverage_gate). Declaring
// the one-method interface here lets tests script structured responses without
// a live model; *agent.Agent satisfies it (sdk/go/agent/agent.go).
type AICaller interface {
	AI(ctx context.Context, prompt string, opts ...ai.Option) (*ai.Response, error)
}

// Compile-time assertions: the real SDK agent satisfies both seams.
var (
	_ AICaller               = (*agent.Agent)(nil)
	_ harnessx.HarnessCaller = (*agent.Agent)(nil)
)

// Deps carries the two injectable capabilities a reasoner may use. Harness is
// required by every reasoner except CoverageGate; AI is required only by
// IntakePhase and CoverageGate (Python's router.app.ai calls).
type Deps struct {
	Harness harnessx.HarnessCaller
	AI      AICaller
}

// maxParseRetries mirrors the Python SDK's max_parse_retries=2
// (agent_ai.py:747): a malformed structured response re-invokes the LLM up to
// two more times before the parse error surfaces.
const maxParseRetries = 2

// aiStructured mirrors Python's `await router.app.ai(prompt, system=...,
// schema=Model)`: a chat call with a structured-output schema, decoded into
// dest with the Python SDK's parse tolerance (agent_ai.py:806-846) — direct
// JSON parse, then a greedy first-'{'-to-last-'}' extraction (Python's
// re.search(r"\{.*\}", text, re.DOTALL) over fenced/prose output), then a
// fresh LLM call, up to maxParseRetries times. Transport/API errors are NOT
// retried here (Python retries only "Could not parse structured response").
// Each attempt decodes into a fresh instance so a failed attempt's partial
// fill never bleeds into the next one. schemaSample is a zero value of the
// schema struct (the Go analogue of passing the pydantic class).
func aiStructured(ctx context.Context, caller AICaller, prompt, system string, schemaSample any, dest any) error {
	if caller == nil {
		return fmt.Errorf("reasoners: AI seam is nil")
	}
	destV := reflect.ValueOf(dest)
	if destV.Kind() != reflect.Pointer || destV.IsNil() {
		return fmt.Errorf("reasoners: aiStructured dest must be a non-nil pointer, got %T", dest)
	}
	var lastErr error
	for attempt := 0; attempt <= maxParseRetries; attempt++ {
		resp, err := caller.AI(ctx, prompt, ai.WithSystem(system), ai.WithSchema(schemaSample))
		if err != nil {
			return err
		}
		text := resp.Text()
		fresh := reflect.New(destV.Type().Elem())
		if err := json.Unmarshal([]byte(text), fresh.Interface()); err == nil {
			destV.Elem().Set(fresh.Elem())
			return nil
		}
		if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
			fresh = reflect.New(destV.Type().Elem())
			if err := json.Unmarshal([]byte(text[start:end+1]), fresh.Interface()); err == nil {
				destV.Elem().Set(fresh.Elem())
				return nil
			}
		}
		lastErr = fmt.Errorf("Could not parse structured response: %s", text)
	}
	return lastErr
}

// dumpMap is the Go analogue of pydantic model_dump(): a JSON round trip that
// yields a map containing every field (the schemas structs carry no omitempty),
// with nil pointers as JSON null.
func dumpMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("reasoners: dump %T: %w", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("reasoners: dump %T: %w", v, err)
	}
	return m, nil
}

// dumpSlice dumps each element like Python's `[x.model_dump() for x in xs]`.
// A nil slice dumps to an empty (non-nil) list — Python list comprehensions
// never yield None.
func dumpSlice[T any](xs []T) ([]any, error) {
	out := make([]any, 0, len(xs))
	for i := range xs {
		m, err := dumpMap(&xs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
