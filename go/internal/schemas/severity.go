// Package schemas ports PR-AF's pydantic schema layer to Go: the input,
// pipeline, gate, and output structs plus the canonical severity vocabulary.
//
// Parity rules (design §B.2 / §C.1):
//   - Every field Python's model_dump() always emits carries a snake_case json
//     tag and NO omitempty (model_dump emits every field, no exclude_none).
//   - Pydantic `X | None` fields map to Go pointers so an unset value marshals
//     to JSON null exactly as Python does.
//   - Structs with a non-zero pydantic default get an UnmarshalJSON that seeds
//     the default before decoding (defaults.go), so an absent key keeps the
//     default while a present key (even false/0/"") overrides it.
package schemas

import (
	"encoding/json"
	"strings"
)

// Severity is the canonical finding-severity vocabulary — always one of
// critical | important | suggestion | nitpick. It ports schemas/severity.py's
// Annotated Literal + BeforeValidator: the type advertises the enum, and its
// UnmarshalJSON coerces stray labels (e.g. "high" -> "important") through
// NormalizeSeverity instead of failing, so a model-emitted synonym can never
// break decoding (the incident that silently swallowed a real review).
type Severity string

// DefaultSeverity is where unknown / empty / non-string labels land. It is the
// mid-low rung: keeps a finding visible without inflating it to a blocker.
const DefaultSeverity Severity = "suggestion"

// ValidSeverities is the four canonical severities, in decreasing urgency.
var ValidSeverities = []Severity{"critical", "important", "suggestion", "nitpick"}

// severityAliases maps common labels models reach for onto the 4-level scale by
// rank. This is the CANONICAL map (schemas/severity.py:46-72). The scoring
// package keeps its OWN, deliberately divergent map (medium->important,
// low->suggestion) — never share the two.
var severityAliases = map[string]Severity{
	// canonical (identity)
	"critical":   "critical",
	"important":  "important",
	"suggestion": "suggestion",
	"nitpick":    "nitpick",
	// critical-tier synonyms
	"blocker": "critical",
	"fatal":   "critical",
	"severe":  "critical",
	// important-tier synonyms (the "high" that caused the incident)
	"high":  "important",
	"error": "important",
	"major": "important",
	// suggestion-tier synonyms
	"medium":   "suggestion",
	"moderate": "suggestion",
	"warning":  "suggestion",
	"warn":     "suggestion",
	// nitpick-tier synonyms
	"low":           "nitpick",
	"minor":         "nitpick",
	"nit":           "nitpick",
	"info":          "nitpick",
	"informational": "nitpick",
	"trivial":       "nitpick",
}

// NormalizeSeverity coerces an arbitrary severity label to the canonical
// vocabulary (schemas/severity.py normalize_severity). It is case-insensitive
// and whitespace-tolerant; empty, non-string, or unrecognized input falls back
// to def. Accepts a plain string or a Severity (mirroring Python's
// isinstance(value, str) which also accepts str subclasses).
func NormalizeSeverity(v any, def Severity) Severity {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case Severity:
		s = string(t)
	default:
		return def
	}
	key := strings.ToLower(strings.TrimSpace(s))
	if key == "" {
		return def
	}
	if canon, ok := severityAliases[key]; ok {
		return canon
	}
	return def
}

// UnmarshalJSON coerces any JSON scalar (string synonym, null, number, …) to the
// canonical vocabulary via NormalizeSeverity — the Go equivalent of the pydantic
// BeforeValidator. A null or non-string value yields DefaultSeverity.
func (s *Severity) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = NormalizeSeverity(v, DefaultSeverity)
	return nil
}
