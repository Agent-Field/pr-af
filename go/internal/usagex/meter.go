// Package usagex carries provider-reported spend across structured fallbacks.
package usagex

import (
	"context"
	"math"
	"sync"

	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"
	"github.com/Agent-Field/pr-af/go/internal/harnessx"
)

type AICaller interface {
	AI(context.Context, string, ...ai.Option) (*ai.Response, error)
}

// Meter belongs to one phase invocation. Nil provider costs remain unknown;
// no prices are inferred from token counts. Parallel calls are safe.
type Meter struct {
	mu    sync.Mutex
	total float64
	known bool
}

func (m *Meter) add(cost *float64) {
	if cost == nil || *cost < 0 || math.IsNaN(*cost) || math.IsInf(*cost, 0) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total += *cost
	m.known = true
}

// Apply adds cost_usd only when a provider supplied a cost, retaining the
// existing map shape for older providers that do not report spend.
func (m *Meter) Apply(out map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.known && out != nil {
		out["cost_usd"] = m.total
	}
}
func (m *Meter) Result() map[string]any {
	out := map[string]any{}
	m.Apply(out)
	return out
}

type meteredAI struct {
	caller AICaller
	meter  *Meter
}

func (m *Meter) AI(caller AICaller) AICaller {
	if caller == nil {
		return nil
	}
	return meteredAI{caller, m}
}
func (a meteredAI) AI(ctx context.Context, prompt string, opts ...ai.Option) (*ai.Response, error) {
	result, err := a.caller.AI(ctx, prompt, opts...)
	if result != nil && result.Usage != nil {
		a.meter.add(result.Usage.Cost)
	}
	return result, err
}

type meteredHarness struct {
	caller harnessx.HarnessCaller
	meter  *Meter
}

func (m *Meter) Harness(caller harnessx.HarnessCaller) harnessx.HarnessCaller {
	if caller == nil {
		return nil
	}
	return meteredHarness{caller, m}
}
func (a meteredHarness) Harness(ctx context.Context, prompt string, schema map[string]any, dest any, opts harness.Options) (*harness.Result, error) {
	result, err := a.caller.Harness(ctx, prompt, schema, dest, opts)
	if result != nil {
		a.meter.add(result.CostUSD)
	}
	return result, err
}
