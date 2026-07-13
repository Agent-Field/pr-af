// Package harnessx is the single choke-point every PR-AF reasoner uses to call
// the AgentField harness. It replaces the Python monkeypatch of app.harness with
// an explicit generic wrapper:
//
//   - schemaFor[T] resolves the JSON-schema map the harness consumes to build
//     its OUTPUT REQUIREMENTS prompt suffix AND to validate parsed output
//     (design §C.3). For destination types registered by RegisterSchema it
//     returns the committed pydantic-generated schema (parity with the Python
//     SDK); for unknown types it falls back to invopop reflection.
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
	"embed"
	"encoding/json"
	"reflect"
	"sync"

	"github.com/invopop/jsonschema"
)

// embeddedSchemas holds the committed pydantic-generated JSON schemas, one per
// destination type that flows through Run[T]. They are produced by
// go/scripts/gen_schemas.py from the exact pydantic models the Python reasoners
// pass to router.app.harness(schema=...), so the schema the Go SDK validates
// against matches Python's own model_json_schema() rendering (defaulted fields
// optional, X|None nullable, extra keys allowed). See that script for the one
// deliberate deviation (the Severity enum is relaxed to a plain string).
//
// go:embed skips files whose names begin with "_" or "." unless the pattern
// uses the all: prefix, so every fixture basename is plain (no leading
// underscore) even for private Python models like _AdversaryPhaseResult.
//
//go:embed testdata/schemas/*.json
var embeddedSchemas embed.FS

// schemaCache memoizes the resolved JSON-schema map per concrete type T so the
// embedded-fixture load (or the non-trivial invopop reflection) runs once per
// type. Keyed by reflect.Type; the stored map[string]any is treated as immutable
// by callers (the harness only ever marshals/reads it, never mutates), so
// sharing the cached value across goroutines is safe.
var schemaCache sync.Map // reflect.Type -> map[string]any

// schemaRegistry maps a destination reflect.Type to the basename (no extension)
// of its committed schema fixture under testdata/schemas/. It is populated by
// RegisterSchema from the reasoners package's init() — that package owns the
// destination types (both the private harness-result structs and the schemas.*
// pipeline types), so registering there keeps harnessx free of an import cycle.
var schemaRegistry sync.Map // reflect.Type -> string (fixture basename)

// RegisterSchema associates a destination type with its committed pydantic
// schema fixture. Call it once per Run[T] destination type (see
// reasoners/schemas_registry.go). Registration must complete before the first
// Run[T] for that type; init()-time registration guarantees this because any
// reasoner that calls Run[T] imports the package whose init() registers T.
func RegisterSchema(t reflect.Type, fixtureName string) {
	schemaRegistry.Store(t, fixtureName)
}

// RegisteredSchema returns the resolved schema for a registered type, or
// (nil, false) if t has no registered fixture. Exported for the drift test,
// which cross-checks every embedded schema against its Go struct's json tags.
func RegisteredSchema(t reflect.Type) (map[string]any, bool) {
	name, ok := schemaRegistry.Load(t)
	if !ok {
		return nil, false
	}
	m, err := loadEmbeddedSchema(name.(string))
	if err != nil {
		return nil, false
	}
	return m, true
}

// loadEmbeddedSchema reads and decodes a committed schema fixture by basename.
func loadEmbeddedSchema(name string) (map[string]any, error) {
	b, err := embeddedSchemas.ReadFile("testdata/schemas/" + name + ".json")
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// schemaFor resolves the JSON-schema map the Go SDK harness consumes for T.
//
// How the SDK consumes this map (verified against the pinned sdk/go
// harness/runner.go + harness/schema.go): the harness validates every parsed
// output against this schema with santhosh-tekuri/jsonschema/v5
// (runSchemaValidation -> validateAgainstSchema) on each parse success, and its
// schema-retry loop fires when validation fails. A JSON round-trip into the dest
// struct alone accepts missing required fields, bad enums, and extra keys
// silently, so this schema is what actually defines validity. The map is ALSO
// pretty-printed into the BuildPromptSuffix / BuildFollowupPrompt OUTPUT
// REQUIREMENTS instruction and read by DiagnoseOutputFailure (map["properties"]).
//
// Because validation is real and strict, the schema must match Python's — an
// invopop reflection (all fields required, non-nullable pointers,
// additionalProperties:false) would reject Python-valid output. So for
// registered types schemaFor returns the committed pydantic schema; only unknown
// types (e.g. ad-hoc test structs) fall back to invopop reflection.
//
// Invopop reflector configuration (fallback path):
//   - ExpandedStruct: inline the root type's own properties at the top level so
//     map["properties"] is populated for DiagnoseOutputFailure.
//   - DoNotReference=false (default): emit a $defs map for nested struct types.
//   - Anonymous: suppress the auto-generated $id derived from the package path.
func schemaFor[T any]() map[string]any {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if cached, ok := schemaCache.Load(t); ok {
		return cached.(map[string]any)
	}

	var m map[string]any
	if name, ok := schemaRegistry.Load(t); ok {
		// Registered destination type: use the committed pydantic schema. A load
		// failure "cannot happen" (go:embed guarantees the file is compiled in
		// and gen_schemas.py + the determinism test guarantee valid JSON), but
		// if it ever did we fall through to invopop rather than panic.
		if loaded, err := loadEmbeddedSchema(name.(string)); err == nil {
			m = loaded
		}
	}
	if m == nil {
		m = reflectSchema(t)
	}

	schemaCache.Store(t, m)
	return m
}

// reflectSchema is the invopop fallback for types with no committed fixture.
func reflectSchema(t reflect.Type) map[string]any {
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
	return m
}
