package reasoners

import "encoding/json"

// strictAISchemaName identifies a strict structured-output schema owned by a
// reasoner. Keeping these definitions next to their callers avoids relying on
// the SDK's incomplete reflection of slice fields.
type strictAISchemaName string

const (
	strictAISchemaIntakeGate   strictAISchemaName = "IntakeGate"
	strictAISchemaCoverageGate strictAISchemaName = "CoverageGate"
)

// strictAISchemas is the source of truth for the schemas passed to the two
// strict .ai() calls. Its raw JSON values are immutable after initialization.
var strictAISchemas = map[strictAISchemaName]json.RawMessage{
	strictAISchemaIntakeGate: json.RawMessage(`{
		"type": "object",
		"properties": {
			"pr_type": {"type": "string"},
			"complexity": {"type": "string"},
			"confident": {"type": "boolean"}
		},
		"required": ["pr_type", "complexity", "confident"],
		"additionalProperties": false
	}`),
	strictAISchemaCoverageGate: json.RawMessage(`{
		"type": "object",
		"properties": {
			"fully_covered": {"type": "boolean"},
			"gap_descriptions": {
				"type": "array",
				"items": {"type": "string"}
			},
			"confident": {"type": "boolean"}
		},
		"required": ["fully_covered", "gap_descriptions", "confident"],
		"additionalProperties": false
	}`),
}
