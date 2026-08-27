package reasoners

import (
	"reflect"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// This file wires every destination type used with harnessx.Run[T] to its
// committed pydantic-generated schema fixture (go/internal/harnessx/testdata/
// schemas/<Name>.json, produced by go/scripts/gen_schemas.py). harnessx.Run's
// schemaFor[T] consults this registry so the Go SDK validates parsed harness
// output against the generated Python schema plus the documented Severity and
// explicit-collection safety adjustments in gen_schemas.py. This preserves
// pydantic parity on nullable/default/extra-key behavior without allowing an
// empty object to impersonate an intentional clean result.
//
// Registration lives HERE, not in harnessx, because the reasoners package owns
// the destination types (the private harness-result structs below plus the
// schemas.* pipeline types) and already imports harnessx — so there is no import
// cycle. init() runs before any Run[T] call: a reasoner that calls Run[T] is in
// this package, whose init() therefore executes first.
//
// The fixture basename is the Python model name with any leading underscore
// stripped (go:embed skips "_"-prefixed filenames). Each mapping is 1:1 with an
// entry in gen_schemas.py's MODELS; the drift test cross-checks both ends.
func init() {
	reg := func(v any, fixture string) {
		harnessx.RegisterSchema(reflect.TypeOf(v), fixture)
	}

	// Private harness-result models (reasoners/harnesses.py -> schemas.go here).
	reg(adversaryPhaseResult{}, "AdversaryPhaseResult")
	reg(anatomySemanticResult{}, "AnatomySemanticResult")
	reg(compoundDedupResult{}, "CompoundDedupResult")
	reg(compoundResult{}, "CompoundResult")
	reg(deepenResult{}, "DeepenResult")
	reg(obligationVerdict{}, "ObligationVerdict")
	reg(obligationsResult{}, "ObligationsResult")
	reg(postWorthinessResult{}, "PostWorthinessResult")
	reg(reviewFindingsResult{}, "ReviewFindingsResult")
	reg(verificationResult{}, "VerificationResult")

	// Public pipeline models (schemas/pipeline.py -> internal/schemas).
	reg(schemas.IntakeResult{}, "IntakeResult")
	reg(schemas.MetaDimensionResult{}, "MetaDimensionResult")
	reg(schemas.ReviewPlan{}, "ReviewPlan")
}
