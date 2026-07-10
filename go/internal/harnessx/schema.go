// Package harnessx is the single choke-point every PR-AF reasoner uses to call
// the AgentField harness. It replaces the Python monkeypatch of app.harness with
// an explicit generic wrapper:
//
//   - schemaFor[T] reflects a Go struct into the JSON-schema map the harness
//     consumes to build its OUTPUT REQUIREMENTS prompt suffix (design §C.3).
//   - Run[T] calls the harness, classifies fatal API errors, and on a schema
//     parse-failure hands the caller a default-seeded value so it can apply its
//     deterministic fallback (design §C.3).
//
// Unlike the SWE-AF harness this is adapted from, PR-AF has no scout-credential
// store, so Run does NOT inject run-scoped credentials into the subprocess env —
// opts is passed through unchanged. This is the ONLY way reasoners should reach
// the harness: it guarantees uniform fatal-error handling across every reasoner.
package harnessx

import (
	"encoding/json"
	"reflect"
	"sync"

	"github.com/invopop/jsonschema"
)

// schemaCache memoizes the reflected JSON-schema map per concrete type T so the
// (non-trivial) reflection + marshal round-trip runs once per type. Keyed by
// reflect.Type; the stored map[string]any is treated as immutable by callers
// (the harness only ever marshals/reads it, never mutates), so sharing the
// cached value across goroutines is safe.
var schemaCache sync.Map // reflect.Type -> map[string]any

// schemaFor reflects T into the JSON-schema map the Go SDK harness consumes.
//
// How the SDK consumes this map (verified against sdk/go/harness/schema.go):
// the map is NOT used for programmatic validation — validity is defined purely
// by json.Unmarshal into the dest struct succeeding. The harness uses the map
// only to (a) embed a pretty-printed schema in the BuildPromptSuffix /
// BuildFollowupPrompt OUTPUT REQUIREMENTS instruction and (b) list expected
// top-level keys in DiagnoseOutputFailure (which reads map["properties"]). Keys
// are alphabetized by json.MarshalIndent, so field ordering and `required`
// completeness are cosmetic. This means invopop's output — with $defs, items,
// and enum — is more than sufficient, and far richer than the SDK's own shallow
// StructToJSONSchema (which drops nested props/items/enums; design §C.3 says do
// NOT use it).
//
// Reflector configuration:
//   - ExpandedStruct: inline the root type's own properties at the top level so
//     map["properties"] is populated for DiagnoseOutputFailure (rather than a
//     bare $ref to $defs).
//   - DoNotReference=false (default): emit a $defs map for nested struct types.
//   - Anonymous: suppress the auto-generated $id derived from the package path.
func schemaFor[T any]() map[string]any {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if cached, ok := schemaCache.Load(t); ok {
		return cached.(map[string]any)
	}

	r := &jsonschema.Reflector{
		ExpandedStruct: true,  // root properties inline at top level
		DoNotReference: false, // emit $defs for nested types
		Anonymous:      true,  // no auto-generated $id from PkgPath
	}
	schema := r.ReflectFromType(t)

	b, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}

	schemaCache.Store(t, m)
	return m
}
