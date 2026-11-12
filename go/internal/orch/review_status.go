package orch

import (
	"context"
	"sort"

	"github.com/Agent-Field/agentfield/sdk/go/harness"
	"github.com/Agent-Field/pr-af/go/internal/harnessx"
)

func (o *Orchestrator) phaseContext(ctx context.Context, name string) context.Context {
	return harnessx.WithResultObserver(ctx, func(result *harness.Result, err error) {
		if err != nil || result == nil || result.IsError || result.Parsed == nil {
			o.markIncomplete(name)
		}
	})
}

func (o *Orchestrator) markIncomplete(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.incomplete == nil {
		o.incomplete = map[string]bool{}
	}
	o.incomplete[name] = true
}

func (o *Orchestrator) incompleteWork() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	missing := make([]string, 0, len(o.incomplete))
	for name := range o.incomplete {
		missing = append(missing, name)
	}
	if o.budgetExhausted {
		missing = append(missing, "review work stopped at budget limit")
	}
	sort.Strings(missing)
	return missing
}
