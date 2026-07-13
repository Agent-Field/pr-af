package harnessx

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// This file proves the HIGH-severity fix: the committed pydantic schemas the
// harness now validates against accept Python-valid output that the old invopop
// reflection rejected. The Go SDK validates parsed output against this schema
// with santhosh-tekuri/jsonschema/v5 (pinned sdk/go harness/schema.go
// validateAgainstSchema), so these tests use the SAME library the SDK uses.

// compileSchema mirrors the SDK's validateAgainstSchema: marshal the map, add it
// as an in-memory resource, compile. It is the exact validation surface the
// harness runs on every parse success.
func compileSchema(t *testing.T, schema map[string]any) *jsonschema.Schema {
	t.Helper()
	b, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://parity/schema.json", bytes.NewReader(b)); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	compiled, err := c.Compile("mem://parity/schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return compiled
}

// validateDoc validates a JSON document string against a compiled schema,
// normalizing through JSON exactly as the SDK does.
func validateDoc(t *testing.T, compiled *jsonschema.Schema, doc string) error {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	return compiled.Validate(v)
}

// pythonValidReviewDoc is shaped like output pydantic's model_validate accepts
// but invopop's reflected schema rejected: a finding that OMITS the defaulted
// fields (tags, confidence, evidence, hunk_context), sets the Optional
// "suggestion" to null, uses a severity SYNONYM ("high", which the Python
// BeforeValidator coerces to "important"), and carries an EXTRA explanatory key
// the model volunteered. sub_reviews is omitted entirely (defaulted).
const pythonValidReviewDoc = `{
  "findings": [
    {
      "dimension_id": "d1",
      "dimension_name": "Retry semantics",
      "file_path": "client.py",
      "line_start": 10,
      "line_end": 12,
      "severity": "high",
      "title": "Retry loop can spin forever",
      "body": "The loop never decrements the counter.",
      "suggestion": null,
      "why_this_matters": "extra explanatory key the model volunteered"
    }
  ],
  "sub_reviews": []
}`

// --- Axis assertions on the committed pydantic schema ------------------------

// Contract: the embedded ReviewFindingsResult schema differs from an invopop
// reflection on exactly the three axes the finding names — defaulted fields are
// optional, X|None is nullable, extra keys are allowed — plus the documented
// Severity relaxation.
func TestEmbeddedSchemaParityAxes(t *testing.T) {
	schema, err := loadEmbeddedSchema("ReviewFindingsResult")
	if err != nil {
		t.Fatalf("load embedded schema: %v", err)
	}

	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("expected $defs, got: %v", schema)
	}
	finding, ok := defs["ReviewFinding"].(map[string]any)
	if !ok {
		t.Fatalf("expected $defs.ReviewFinding, got: %v", defs)
	}

	// Axis 1 — required excludes every defaulted field.
	required := toStringSet(finding["required"])
	for _, f := range []string{"tags", "confidence", "evidence", "hunk_context", "suggestion"} {
		if required[f] {
			t.Errorf("defaulted field %q must NOT be required (invopop marked it so; pydantic does not)", f)
		}
	}
	// Sanity: fields without a pydantic default remain required.
	for _, f := range []string{"dimension_id", "file_path", "severity", "title", "body"} {
		if !required[f] {
			t.Errorf("no-default field %q should be required", f)
		}
	}

	props, _ := finding["properties"].(map[string]any)

	// Axis 2 — the Optional "suggestion" is nullable (anyOf includes type:null).
	suggestion, _ := props["suggestion"].(map[string]any)
	if !anyOfAllowsNull(suggestion) {
		t.Errorf("suggestion must be nullable (anyOf with type:null); got %v", suggestion)
	}

	// Axis 3 — no additionalProperties:false anywhere (extra keys allowed).
	if hasAdditionalPropertiesFalse(schema) {
		t.Errorf("schema must not set additionalProperties:false (pydantic ignores extra keys)")
	}

	// Documented deviation — Severity is relaxed to a plain string (no strict
	// enum), matching the BeforeValidator's accept-and-coerce semantics.
	severity, _ := props["severity"].(map[string]any)
	if _, hasEnum := severity["enum"]; hasEnum {
		t.Errorf("severity must NOT carry a strict enum (relaxed for BeforeValidator parity); got %v", severity)
	}
	if severity["type"] != "string" {
		t.Errorf("severity should remain type:string; got %v", severity["type"])
	}
}

// --- Behavioral proof: pydantic schema accepts, invopop schema rejects -------

// Contract: the committed pydantic schema ACCEPTS Python-valid output that the
// invopop reflection REJECTS. This is the fix, demonstrated on the real
// validation surface (santhosh-tekuri) the SDK uses.
func TestPythonValidOutputAcceptedByEmbeddedSchema(t *testing.T) {
	schema, err := loadEmbeddedSchema("ReviewFindingsResult")
	if err != nil {
		t.Fatalf("load embedded schema: %v", err)
	}
	compiled := compileSchema(t, schema)
	if err := validateDoc(t, compiled, pythonValidReviewDoc); err != nil {
		t.Fatalf("pydantic schema must ACCEPT Python-valid output, but rejected it: %v", err)
	}
}

// mirrorFinding / mirrorFindingsResult reproduce the Go destination shape of
// reviewFindingsResult so the invopop fallback (unregistered types) emits the
// exact schema the port shipped BEFORE this fix — all fields required, the
// *string pointer non-nullable, additionalProperties:false. Validating the same
// Python-valid doc against it must FAIL; that failure is the bug the fix closes.
type mirrorFinding struct {
	DimensionID   string   `json:"dimension_id"`
	DimensionName string   `json:"dimension_name"`
	FilePath      string   `json:"file_path"`
	LineStart     int      `json:"line_start"`
	LineEnd       int      `json:"line_end"`
	HunkContext   string   `json:"hunk_context"`
	Severity      string   `json:"severity"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`
	Suggestion    *string  `json:"suggestion"`
	Evidence      string   `json:"evidence"`
	Confidence    float64  `json:"confidence"`
	Tags          []string `json:"tags"`
}

type mirrorFindingsResult struct {
	Findings   []mirrorFinding `json:"findings"`
	SubReviews []string        `json:"sub_reviews"`
}

// Contract (the guard the fix exists for): the OLD invopop-reflected schema
// rejects the very output pydantic accepts. If this ever starts passing, the
// invopop fallback has silently stopped being strict and the regression the fix
// addresses could resurface unnoticed.
func TestPythonValidOutputRejectedByInvopopSchema(t *testing.T) {
	// mirrorFindingsResult is NOT registered, so schemaFor takes the invopop
	// fallback — the pre-fix behavior, reproduced exactly.
	invopop := schemaFor[mirrorFindingsResult]()

	// Make the premise explicit: invopop over-constrains on the three axes.
	if !hasAdditionalPropertiesFalse(invopop) {
		t.Fatalf("premise broken: expected invopop schema to set additionalProperties:false")
	}

	compiled := compileSchema(t, invopop)
	if err := validateDoc(t, compiled, pythonValidReviewDoc); err == nil {
		t.Fatalf("invopop schema must REJECT Python-valid output (that rejection is the bug the fix closes)")
	}
}

// --- helpers ----------------------------------------------------------------

func toStringSet(v any) map[string]bool {
	out := map[string]bool{}
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

func anyOfAllowsNull(node map[string]any) bool {
	anyOf, ok := node["anyOf"].([]any)
	if !ok {
		return false
	}
	for _, o := range anyOf {
		if m, ok := o.(map[string]any); ok && m["type"] == "null" {
			return true
		}
	}
	return false
}

// hasAdditionalPropertiesFalse reports whether any object node in the schema
// (root or any $defs entry, recursively) sets additionalProperties:false.
func hasAdditionalPropertiesFalse(node any) bool {
	switch n := node.(type) {
	case map[string]any:
		if ap, ok := n["additionalProperties"]; ok {
			if b, ok := ap.(bool); ok && !b {
				return true
			}
		}
		for _, v := range n {
			if hasAdditionalPropertiesFalse(v) {
				return true
			}
		}
	case []any:
		for _, v := range n {
			if hasAdditionalPropertiesFalse(v) {
				return true
			}
		}
	}
	return false
}
