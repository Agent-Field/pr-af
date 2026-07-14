// Package afx holds small ergonomics over the AgentField Go SDK that every
// reasoner handler in the port reuses.
package afx

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Bind decodes a reasoner's untyped input map into a typed value T.
//
// Handlers registered with the SDK receive input as map[string]any. Bind
// round-trips that map through JSON (marshal then unmarshal into T), which
// mirrors how the Python port materializes a Pydantic model from the request
// body: field-name matching is by the json struct tags (the exact snake_case
// pydantic field names), and any custom UnmarshalJSON on T runs — so a T whose
// UnmarshalJSON seeds non-zero Pydantic defaults gets those defaults for keys
// absent from input (design §2.2, §8).
//
// Plain encoding/json is deliberate: no json.Decoder/UseNumber. Numbers in the
// input map are already Go float64/int (they came from the SDK's own JSON
// decode or from a Go caller), and re-marshaling then unmarshaling into the
// typed fields of T yields the correct concrete types without number-precision
// gymnastics (design §8).
func Bind[T any](input map[string]any) (T, error) {
	var out T
	b, err := json.Marshal(input)
	if err != nil {
		return out, fmt.Errorf("afx.Bind: marshal input: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("afx.Bind: unmarshal into %T: %w", out, err)
	}
	return out, nil
}

// ToMap is Bind's inverse: it renders a typed input struct as the
// map[string]any shape the SDK's reasoner handlers (and Agent.CallLocal)
// accept. Top-level exported fields become map entries keyed by their json
// tag, and the field VALUES stay typed — deliberately NOT a marshal→unmarshal
// round trip, which would decode nested values into plain Go maps and lose
// what their custom marshalers encode. The concrete casualty would be
// reasoners.OrderedPatches: its object-order MarshalJSON preserves Python's
// dict insertion order, but flattened into a map[string]any it re-marshals
// with sorted keys and the meta/obligation prompts would render patches in
// the wrong order. Keeping values typed lets Bind on the handler side (and
// the SDK's workflow-event emitter) re-marshal them through the same custom
// marshalers, so ToMap→Bind is lossless.
//
// The reasoner input structs are flat, fully json-tagged, and carry no
// omitempty (every key is emitted, so Bind-side default seeding never
// overrides a deliberately zero field); ToMap ignores omitempty accordingly.
// Anonymous embedded structs without their own json tag are flattened the way
// encoding/json flattens them.
func ToMap(v any) (map[string]any, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, fmt.Errorf("afx.ToMap: nil %T", v)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("afx.ToMap: %T is not a struct", v)
	}
	out := make(map[string]any, rv.NumField())
	fillMap(out, rv)
	return out, nil
}

// fillMap writes rv's fields into out, recursing through untagged anonymous
// struct fields (encoding/json flattening).
func fillMap(out map[string]any, rv reflect.Value) {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			if f.Anonymous {
				fv := rv.Field(i)
				for fv.Kind() == reflect.Pointer && !fv.IsNil() {
					fv = fv.Elem()
				}
				if fv.Kind() == reflect.Struct {
					fillMap(out, fv)
					continue
				}
			}
			name = f.Name
		}
		out[name] = rv.Field(i).Interface()
	}
}
