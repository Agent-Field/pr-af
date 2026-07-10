package orch

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// badInputError wraps ErrBadInput (for errors.Is classification → HTTP 400) but
// reports ONLY the raw message via Error(), so the node's `{"error": <msg>}`
// body is byte-identical to Python's str(ValueError) — not "bad input: <msg>".
type badInputError struct{ msg string }

func (e *badInputError) Error() string { return e.msg }
func (e *badInputError) Unwrap() error { return ErrBadInput }

func badInput(msg string) error { return &badInputError{msg: msg} }

// budgetError wraps errBudgetExhausted (a non-ValueError → HTTP 500) while
// reporting the raw message, matching str(BudgetExhaustedError).
type budgetError struct{ msg string }

func (e *budgetError) Error() string { return e.msg }
func (e *budgetError) Unwrap() error { return errBudgetExhausted }

func budgetExhaustedErr(msg string) error { return &budgetError{msg: msg} }

// itoa is strconv.Itoa, aliased for terse positional-id formatting.
func itoa(i int) string { return strconv.Itoa(i) }

// structToMap JSON-round-trips a struct into a decoded map — the Go analogue of
// model_dump() when a map (not a struct) is required (metadata dicts, evidence
// packages passed to reasoners).
func structToMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// asObjListStrict returns the objects at m[key] when that value IS a JSON array
// (mirroring Python's isinstance(payload.get(key), list) branch selection): a
// non-array yields nil, an array yields a non-nil slice with only its object
// elements (non-dicts dropped, exactly as the Python loops `continue` on them).
func asObjListStrict(m map[string]any, key string) []map[string]any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if mp, ok := e.(map[string]any); ok {
			out = append(out, mp)
		}
	}
	return out
}

// truthy mirrors Python bool() over decoded-JSON types.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return false
	}
}

// getBoolDefault mirrors bool(m.get(key, def)): absent → def, present → truthy.
func getBoolDefault(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok {
		return def
	}
	return truthy(v)
}

// mapGet mirrors m.get(key, def): the raw value if present, else def.
func mapGet(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// severityOr mirrors v.get("severity", def), returning the raw value so the
// downstream Severity.UnmarshalJSON normalizes it.
func severityOr(m map[string]any, def string) any {
	if v, ok := m["severity"]; ok {
		return v
	}
	return def
}

// strp dereferences a *string (nil → "").
func strp(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// countSplitlines counts lines the way Python str.splitlines() does: a trailing
// newline does NOT yield a trailing empty element. Used by _resolve_depth's
// diff.splitlines() line count.
func countSplitlines(s string) int {
	if s == "" {
		return 0
	}
	// splitlines splits on \n, \r, \r\n and drops a single trailing separator.
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	n := strings.Count(normalized, "\n")
	if !strings.HasSuffix(normalized, "\n") {
		n++
	}
	return n
}

// unwrap ports orchestrator._unwrap: prefer result["output"], then
// result["result"], each only when it is itself a JSON object; else the map
// itself. Ported reasoners never wrap, so this is identity in practice.
func unwrap(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	if v, ok := result["output"]; ok {
		if m, ok := v.(map[string]any); ok {
			return m
		}
	}
	if v, ok := result["result"]; ok {
		if m, ok := v.(map[string]any); ok {
			return m
		}
	}
	return result
}

// asFloat coerces a JSON scalar to float64 (int/float only, mirroring Python's
// isinstance(cost, (int, float))). Booleans are excluded (Python's isinstance
// against (int,float) matches bool, but cost_usd is never a bool and JSON
// decodes numbers to float64 anyway).
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// getStr mirrors Python dict.get(key, default) restricted to strings.
func getStr(m map[string]any, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// getIntOrZero mirrors `int(m.get(key, 0) or 0)`: a falsy value (missing, nil, 0,
// "", false) yields 0; otherwise the value is coerced to int.
func getIntOrZero(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int(t)
	case int:
		return t
	case string:
		if t == "" {
			return 0
		}
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return int(f)
		}
		return 0
	case bool:
		if t {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// getFloatOr mirrors `float(m.get(key, def) or def)`: a falsy value yields def.
func getFloatOr(m map[string]any, key string, def float64) float64 {
	v, ok := m[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case nil:
		return def
	case float64:
		if t == 0 {
			return def
		}
		return t
	case int:
		if t == 0 {
			return def
		}
		return float64(t)
	case string:
		if t == "" {
			return def
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f
		}
		return def
	default:
		return def
	}
}

// getStrSlice extracts a []string from m[key], dropping non-string elements
// (mirrors item.get("tags", []) feeding a list[str] pydantic field).
func getStrSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return []string{}
	}
	arr, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// asObjList extracts the []map[string]any at m[key] (skipping non-objects), the
// Go analogue of a Python `payload.get(key)` that isinstance-checks list-of-dict.
func asObjList(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// getIntSlice extracts a []int from a JSON array of numbers (keep_indices).
func getIntSlice(v any) []int {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(arr))
	for _, e := range arr {
		switch t := e.(type) {
		case float64:
			out = append(out, int(t))
		case int:
			out = append(out, t)
		}
	}
	return out
}

// mapToReviewFinding ports the model_validate(normalized) step in
// _extract_findings / _extract_compound_findings: it applies the per-key
// defaults, then JSON-round-trips into a ReviewFinding so the struct's
// UnmarshalJSON seeding and Severity normalization run exactly as pydantic does.
func mapToReviewFinding(item map[string]any, defaults map[string]any) schemas.ReviewFinding {
	normalized := map[string]any{
		"dimension_id":   getStr(item, "dimension_id", strDefault(defaults, "dimension_id")),
		"dimension_name": getStr(item, "dimension_name", strDefault(defaults, "dimension_name")),
		"file_path":      getStr(item, "file_path", ""),
		"line_start":     getIntOrZero(item, "line_start"),
		"line_end":       lineEnd(item, defaults),
		"hunk_context":   getStr(item, "hunk_context", ""),
		"severity":       severityDefault(item, defaults),
		"title":          getStr(item, "title", strDefault(defaults, "title")),
		"body":           getStr(item, "body", ""),
		"suggestion":     item["suggestion"],
		"evidence":       getStr(item, "evidence", ""),
		"confidence":     getFloatOr(item, "confidence", 0.5),
		"tags":           getStrSlice(item, "tags"),
	}
	b, _ := json.Marshal(normalized)
	var f schemas.ReviewFinding
	_ = json.Unmarshal(b, &f)
	return f
}

func strDefault(defaults map[string]any, key string) string {
	if defaults == nil {
		return ""
	}
	if s, ok := defaults[key].(string); ok {
		return s
	}
	return ""
}

// lineEnd handles the compound path's `int(item.get("line_end", item.get(
// "line_start", 0)) or 0)`: default line_end falls back to line_start.
func lineEnd(item map[string]any, defaults map[string]any) int {
	if _, present := item["line_end"]; present {
		return getIntOrZero(item, "line_end")
	}
	if defaults != nil {
		if v, ok := defaults["line_end_fallback"]; ok && v == "line_start" {
			return getIntOrZero(item, "line_start")
		}
	}
	return getIntOrZero(item, "line_end")
}

// mapToStruct JSON-round-trips a decoded map into a typed struct, running the
// struct's UnmarshalJSON default-seeding — the Go analogue of
// SomeModel.model_validate(m).
func mapToStruct[T any](m map[string]any) T {
	var v T
	b, _ := json.Marshal(m)
	_ = json.Unmarshal(b, &v)
	return v
}

// truncateRunes returns the first n code points of s (Python's s[:n]).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// stringSet builds a membership set from a slice.
func stringSet(xs []string) map[string]struct{} {
	s := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		s[x] = struct{}{}
	}
	return s
}

func severityDefault(item map[string]any, defaults map[string]any) any {
	if v, ok := item["severity"]; ok {
		return v
	}
	if defaults != nil {
		if s, ok := defaults["severity"].(string); ok {
			return s
		}
	}
	return "suggestion"
}
