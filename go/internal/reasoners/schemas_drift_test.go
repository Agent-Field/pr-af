package reasoners

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Drift guard for the committed pydantic schema fixtures. Every destination type
// registered in schemas_registry.go is validated on two fronts:
//
//  1. Its type is actually registered (harnessx.RegisteredSchema resolves).
//  2. Every OBJECT node in the embedded schema — the root and each $defs entry —
//     has a property-key set that EXACTLY matches the json tags of the Go struct
//     it decodes into.
//
// (2) is the load-bearing check: if someone adds/removes/renames a field on a Go
// struct (or its Python source) without regenerating the fixtures, the key sets
// diverge and this test fails loudly, pointing at the exact type. It also proves
// the standing invariant the fix depends on — every schema property is decodable
// into the struct and every struct field is described by the schema.

// rootTypes maps each registered destination type to its embedded fixture.
var rootTypes = map[reflect.Type]string{
	reflect.TypeOf(adversaryPhaseResult{}):        "AdversaryPhaseResult",
	reflect.TypeOf(anatomySemanticResult{}):       "AnatomySemanticResult",
	reflect.TypeOf(compoundDedupResult{}):         "CompoundDedupResult",
	reflect.TypeOf(compoundResult{}):              "CompoundResult",
	reflect.TypeOf(deepenResult{}):                "DeepenResult",
	reflect.TypeOf(obligationVerdict{}):           "ObligationVerdict",
	reflect.TypeOf(obligationsResult{}):           "ObligationsResult",
	reflect.TypeOf(postWorthinessResult{}):        "PostWorthinessResult",
	reflect.TypeOf(reviewFindingsResult{}):        "ReviewFindingsResult",
	reflect.TypeOf(verificationResult{}):          "VerificationResult",
	reflect.TypeOf(schemas.IntakeResult{}):        "IntakeResult",
	reflect.TypeOf(schemas.MetaDimensionResult{}): "MetaDimensionResult",
	reflect.TypeOf(schemas.ReviewPlan{}):          "ReviewPlan",
}

// defTypes maps each nested $defs title (as pydantic emits it — private models
// keep their leading underscore in the JSON, unlike the fixture filenames) to
// the Go struct that $def decodes into. A $defs entry with no mapping here fails
// the test, forcing every nested type to be accounted for.
var defTypes = map[string]reflect.Type{
	"AdversaryResult":   reflect.TypeOf(schemas.AdversaryResult{}),
	"BudgetAllocation":  reflect.TypeOf(schemas.BudgetAllocation{}),
	"ReviewDimension":   reflect.TypeOf(schemas.ReviewDimension{}),
	"ReviewFinding":     reflect.TypeOf(schemas.ReviewFinding{}),
	"_CompoundFinding":  reflect.TypeOf(compoundFinding{}),
	"_DeepenFinding":    reflect.TypeOf(deepenFinding{}),
	"_Obligation":       reflect.TypeOf(obligation{}),
	"_SubReviewRequest": reflect.TypeOf(schemas.SubReviewRequest{}),
	"_VerifiedFinding":  reflect.TypeOf(verifiedFinding{}),
}

func TestEmbeddedSchemasMatchGoStructs(t *testing.T) {
	for rt, fixture := range rootTypes {
		rt, fixture := rt, fixture
		t.Run(fixture, func(t *testing.T) {
			schema, ok := harnessx.RegisteredSchema(rt)
			if !ok {
				t.Fatalf("%s (%s) is not registered — schemas_registry.go and gen_schemas.go are out of sync",
					rt.Name(), fixture)
			}

			// Root object node ↔ the registered struct.
			assertPropsMatchTags(t, "root", schemaPropertyKeys(schema), structTagKeys(rt))

			// Every $defs object node ↔ its mapped struct.
			defs, _ := schema["$defs"].(map[string]any)
			for name, raw := range defs {
				node, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				dt, mapped := defTypes[name]
				if !mapped {
					t.Fatalf("$defs.%s has no Go struct mapping in defTypes — add it so drift is caught", name)
				}
				assertPropsMatchTags(t, "$defs."+name, schemaPropertyKeys(node), structTagKeys(dt))
			}
		})
	}
}

// assertPropsMatchTags asserts bidirectional set equality between a schema
// node's property names and a struct's json tag names.
func assertPropsMatchTags(t *testing.T, where string, schemaKeys, tagKeys map[string]bool) {
	t.Helper()
	for k := range schemaKeys {
		if !tagKeys[k] {
			t.Errorf("%s: schema property %q has no matching json tag on the Go struct (undecodable / stale fixture?)", where, k)
		}
	}
	for k := range tagKeys {
		if !schemaKeys[k] {
			t.Errorf("%s: struct json tag %q is absent from the schema (regenerate fixtures via gen_schemas.py?)", where, k)
		}
	}
}

// schemaPropertyKeys returns the property-name set of an object schema node.
func schemaPropertyKeys(node map[string]any) map[string]bool {
	out := map[string]bool{}
	props, _ := node["properties"].(map[string]any)
	for k := range props {
		out[k] = true
	}
	return out
}

// structTagKeys returns the json-tag names of a struct type's fields, skipping
// json:"-" and honoring the ",omitempty"/",string" option suffixes.
func structTagKeys(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := tag
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			name = tag[:comma]
		}
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	return out
}

// TestAllRunDestinationsRegistered is a lightweight cross-check that the number
// of registered fixtures matches the number of rootTypes the drift test walks,
// so a newly added Run[T] destination cannot silently skip registration.
func TestRootTypeCountMatchesFixtures(t *testing.T) {
	names := make([]string, 0, len(rootTypes))
	for _, n := range rootTypes {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != 13 {
		t.Fatalf("expected 13 registered Run[T] destination fixtures, got %d: %v", len(names), names)
	}
}
