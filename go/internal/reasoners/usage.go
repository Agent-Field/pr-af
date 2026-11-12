package reasoners

import "github.com/Agent-Field/pr-af/go/internal/usagex"

func meteredDeps(deps Deps) (Deps, *usagex.Meter) {
	meter := &usagex.Meter{}
	deps.Harness = meter.Harness(deps.Harness)
	deps.AI = meter.AI(deps.AI)
	return deps, meter
}
