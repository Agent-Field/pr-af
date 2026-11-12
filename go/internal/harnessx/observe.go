package harnessx

import (
	"context"

	"github.com/Agent-Field/agentfield/sdk/go/harness"
)

type resultObserverKey struct{}

// WithResultObserver carries invocation diagnostics through same-process child
// reasoners without changing their existing fallback result maps. The observer
// can be called concurrently by fan-outs and must synchronize its own state.
func WithResultObserver(ctx context.Context, observer func(*harness.Result, error)) context.Context {
	return context.WithValue(ctx, resultObserverKey{}, observer)
}

func observeResult(ctx context.Context, result *harness.Result, err error) {
	if observer, ok := ctx.Value(resultObserverKey{}).(func(*harness.Result, error)); ok {
		observer(result, err)
	}
}
