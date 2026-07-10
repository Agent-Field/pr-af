package reasoners

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
)

// Contract: OrderedPatches decodes a JSON object preserving document key order
// (Python dict semantics), with duplicate keys last-value-wins first-position,
// and null -> nil.
func TestOrderedPatchesUnmarshal(t *testing.T) {
	var p OrderedPatches
	if err := json.Unmarshal([]byte(`{"z.go":"1","a.go":"2","m.go":"3","z.go":"9"}`), &p); err != nil {
		t.Fatal(err)
	}
	want := OrderedPatches{
		{Key: "z.go", Val: "9"}, {Key: "a.go", Val: "2"}, {Key: "m.go", Val: "3"},
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("got %v, want %v", p, want)
	}

	var nilP OrderedPatches
	if err := json.Unmarshal([]byte(`null`), &nilP); err != nil {
		t.Fatal(err)
	}
	if nilP != nil {
		t.Fatalf("null should decode to nil, got %v", nilP)
	}

	if err := json.Unmarshal([]byte(`[1,2]`), &p); err == nil {
		t.Fatal("array should be rejected")
	}
}

// Contract: OrderedPatches marshals back to an object in slice order.
func TestOrderedPatchesMarshalRoundTrip(t *testing.T) {
	p := OrderedPatches{{Key: "b.go", Val: "x"}, {Key: "a.go", Val: "y\n"}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"b.go":"x","a.go":"y\n"}` {
		t.Fatalf("got %s", b)
	}
	var back OrderedPatches
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) {
		t.Fatalf("round trip: %v != %v", back, p)
	}
}

// Contract: input structs seed their Python keyword defaults on decode
// (depth="standard", max_depth=2) while present keys override.
func TestInputDefaultSeeding(t *testing.T) {
	var intake IntakeInput
	if err := json.Unmarshal([]byte(`{}`), &intake); err != nil {
		t.Fatal(err)
	}
	if intake.Depth != "standard" {
		t.Fatalf("IntakeInput.Depth = %q", intake.Depth)
	}
	if err := json.Unmarshal([]byte(`{"depth":"auto"}`), &intake); err != nil {
		t.Fatal(err)
	}
	if intake.Depth != "auto" {
		t.Fatalf("override lost: %q", intake.Depth)
	}

	var planning PlanningInput
	if err := json.Unmarshal([]byte(`{}`), &planning); err != nil {
		t.Fatal(err)
	}
	if planning.Depth != "standard" {
		t.Fatalf("PlanningInput.Depth = %q", planning.Depth)
	}

	var meta MetaInput
	if err := json.Unmarshal([]byte(`{"diff_patches":{"b.go":"2","a.go":"1"}}`), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Depth != "standard" {
		t.Fatalf("MetaInput.Depth = %q", meta.Depth)
	}
	wantPatches := OrderedPatches{{Key: "b.go", Val: "2"}, {Key: "a.go", Val: "1"}}
	if !reflect.DeepEqual(meta.DiffPatches, wantPatches) {
		t.Fatalf("MetaInput.DiffPatches = %v", meta.DiffPatches)
	}

	var rd ReviewDimensionInput
	if err := json.Unmarshal([]byte(`{"review_prompt":"p"}`), &rd); err != nil {
		t.Fatal(err)
	}
	if rd.MaxDepth != 2 || rd.CurrentDepth != 0 {
		t.Fatalf("ReviewDimensionInput seeds: %+v", rd)
	}
	if err := json.Unmarshal([]byte(`{"max_depth":0}`), &rd); err != nil {
		t.Fatal(err)
	}
	if rd.MaxDepth != 0 { // present zero must override the seed
		t.Fatalf("present max_depth=0 lost: %+v", rd)
	}
}

// Contract: prompts.StrPair and OrderedPatches interconvert by simple slice
// conversion (the builders take []StrPair).
func TestOrderedPatchesConvertsToStrPairs(t *testing.T) {
	p := OrderedPatches{{Key: "a", Val: "1"}}
	pairs := []prompts.StrPair(p)
	if len(pairs) != 1 || pairs[0].Key != "a" {
		t.Fatalf("got %v", pairs)
	}
}
