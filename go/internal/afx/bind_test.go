package afx

import (
	"encoding/json"
	"testing"
)

// coderResultLike mimics the schema-package pattern (design §2.2): a type whose
// UnmarshalJSON seeds a non-zero default (Complete=true) so that a key absent
// from the input keeps the Pydantic default rather than the Go zero value.
type coderResultLike struct {
	Complete    bool   `json:"complete"`
	Summary     string `json:"summary"`
	TestsPassed *bool  `json:"tests_passed"`
}

func defaultCoderResultLike() coderResultLike { return coderResultLike{Complete: true} }

func (c *coderResultLike) UnmarshalJSON(b []byte) error {
	*c = defaultCoderResultLike()
	type alias coderResultLike
	return json.Unmarshal(b, (*alias)(c))
}

type nested struct {
	Name  string          `json:"name"`
	Inner coderResultLike `json:"inner"`
}

// TestBind_DefaultSeeding covers the contract:
//   - absent key keeps the type's UnmarshalJSON default (Bind must route through it)
//   - present key overrides the default, even with the zero value (false)
//   - matching keys populate fields; extra input keys are ignored
func TestBind_DefaultSeeding(t *testing.T) {
	tests := []struct {
		name         string
		input        map[string]any
		wantComplete bool
		wantSummary  string
	}{
		{
			name:         "absent complete keeps seeded default true",
			input:        map[string]any{"summary": "did the thing"},
			wantComplete: true,
			wantSummary:  "did the thing",
		},
		{
			name:         "present complete=false overrides default",
			input:        map[string]any{"complete": false, "summary": "wip"},
			wantComplete: false,
			wantSummary:  "wip",
		},
		{
			name:         "present complete=true stays true",
			input:        map[string]any{"complete": true},
			wantComplete: true,
			wantSummary:  "",
		},
		{
			name:         "empty input keeps all defaults",
			input:        map[string]any{},
			wantComplete: true,
			wantSummary:  "",
		},
		{
			name:         "nil input keeps all defaults",
			input:        nil,
			wantComplete: true,
			wantSummary:  "",
		},
		{
			name:         "unknown keys are ignored",
			input:        map[string]any{"summary": "s", "not_a_field": 42},
			wantComplete: true,
			wantSummary:  "s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Bind[coderResultLike](tt.input)
			if err != nil {
				t.Fatalf("Bind returned error: %v", err)
			}
			if got.Complete != tt.wantComplete {
				t.Errorf("Complete = %v, want %v", got.Complete, tt.wantComplete)
			}
			if got.Summary != tt.wantSummary {
				t.Errorf("Summary = %q, want %q", got.Summary, tt.wantSummary)
			}
		})
	}
}

// TestBind_PointerTriState covers *bool tri-state fields: absent -> nil,
// present -> non-nil pointing at the given value (design §2.1).
func TestBind_PointerTriState(t *testing.T) {
	absent, err := Bind[coderResultLike](map[string]any{})
	if err != nil {
		t.Fatalf("Bind(absent) error: %v", err)
	}
	if absent.TestsPassed != nil {
		t.Errorf("TestsPassed = %v, want nil when absent", *absent.TestsPassed)
	}

	present, err := Bind[coderResultLike](map[string]any{"tests_passed": false})
	if err != nil {
		t.Fatalf("Bind(present) error: %v", err)
	}
	if present.TestsPassed == nil {
		t.Fatalf("TestsPassed = nil, want non-nil pointer to false")
	}
	if *present.TestsPassed != false {
		t.Errorf("*TestsPassed = %v, want false", *present.TestsPassed)
	}
}

// TestBind_Nested covers a nested struct field binding, including the nested
// type's own default-seeding.
func TestBind_Nested(t *testing.T) {
	got, err := Bind[nested](map[string]any{
		"name":  "issue-1",
		"inner": map[string]any{"summary": "nested summary"},
	})
	if err != nil {
		t.Fatalf("Bind error: %v", err)
	}
	if got.Name != "issue-1" {
		t.Errorf("Name = %q, want %q", got.Name, "issue-1")
	}
	if !got.Inner.Complete {
		t.Errorf("Inner.Complete = false, want true (nested default seeding)")
	}
	if got.Inner.Summary != "nested summary" {
		t.Errorf("Inner.Summary = %q, want %q", got.Inner.Summary, "nested summary")
	}
}

// TestBind_IntoMap covers binding into map[string]any (identity round-trip of
// the request body — used where a handler wants the raw shape back).
func TestBind_IntoMap(t *testing.T) {
	in := map[string]any{"a": "x", "b": float64(2)}
	got, err := Bind[map[string]any](in)
	if err != nil {
		t.Fatalf("Bind error: %v", err)
	}
	if got["a"] != "x" {
		t.Errorf("got[a] = %v, want x", got["a"])
	}
	if got["b"] != float64(2) {
		t.Errorf("got[b] = %v (%T), want float64(2)", got["b"], got["b"])
	}
}

// TestBind_TypeMismatch covers the error path: a value whose JSON type cannot
// unmarshal into the target field type returns an error, not a silent zero.
func TestBind_TypeMismatch(t *testing.T) {
	type numeric struct {
		Count int `json:"count"`
	}
	_, err := Bind[numeric](map[string]any{"count": "not-a-number"})
	if err == nil {
		t.Fatalf("Bind = nil error, want unmarshal error for string into int")
	}
}

// orderedVal is a marshaler-carrying type standing in for
// reasoners.OrderedPatches: MarshalJSON controls its bytes. ToMap must keep
// values typed so this marshaler still runs when the map is re-marshaled —
// the property that preserves diff_patches insertion order through the
// CallLocal round trip.
type orderedVal struct{ order []string }

func (o orderedVal) MarshalJSON() ([]byte, error) {
	out := "{"
	for i, k := range o.order {
		if i > 0 {
			out += ","
		}
		b, _ := json.Marshal(k)
		out += string(b) + ":1"
	}
	return []byte(out + "}"), nil
}

// TestToMap_KeepsValuesTyped proves ToMap does not flatten field values into
// plain maps: the custom marshaler's byte order survives a re-marshal of the
// produced map.
func TestToMap_KeepsValuesTyped(t *testing.T) {
	type in struct {
		Patches orderedVal `json:"diff_patches"`
		Depth   string     `json:"depth"`
	}
	m, err := ToMap(in{Patches: orderedVal{order: []string{"z.go", "a.go"}}, Depth: "quick"})
	if err != nil {
		t.Fatalf("ToMap: %v", err)
	}
	if m["depth"] != "quick" {
		t.Errorf("depth = %v, want quick", m["depth"])
	}
	b, err := json.Marshal(m["diff_patches"])
	if err != nil {
		t.Fatalf("re-marshal diff_patches: %v", err)
	}
	if string(b) != `{"z.go":1,"a.go":1}` {
		t.Errorf("diff_patches bytes = %s, want insertion order preserved", b)
	}
}

// TestToMap_TagHandling covers tag name selection, json:"-" skipping,
// unexported skipping, untagged-field fallback to the Go name, and anonymous
// embedded flattening.
func TestToMap_TagHandling(t *testing.T) {
	type Embedded struct {
		Inner string `json:"inner"`
	}
	type in struct {
		Embedded
		Named   string `json:"named_key"`
		Skipped string `json:"-"`
		Bare    string
		hidden  string //nolint:unused // proves unexported fields are skipped
	}
	m, err := ToMap(&in{Embedded: Embedded{Inner: "i"}, Named: "n", Skipped: "s", Bare: "b"})
	if err != nil {
		t.Fatalf("ToMap: %v", err)
	}
	want := map[string]any{"inner": "i", "named_key": "n", "Bare": "b"}
	if len(m) != len(want) {
		t.Fatalf("map = %#v, want %#v", m, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%q] = %v, want %v", k, m[k], v)
		}
	}
}

// TestToMap_Errors covers the non-struct and nil-pointer error paths.
func TestToMap_Errors(t *testing.T) {
	if _, err := ToMap([]string{"not", "a", "struct"}); err == nil {
		t.Error("ToMap(slice) = nil error, want non-struct error")
	}
	var p *struct{}
	if _, err := ToMap(p); err == nil {
		t.Error("ToMap(nil pointer) = nil error, want nil error message")
	}
}
