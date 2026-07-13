package prompts

import (
	"strconv"
	"strings"
)

// This file reproduces Python's json.dumps(obj, default=str) exactly, for the
// context blocks the reasoners interpolate into their prompts. The two facts the
// stdlib encoding/json gets "wrong" relative to Python that we must match:
//   - separators: json.dumps defaults to ", " and ": " (Go uses "," and ":").
//   - key order: Python dicts preserve insertion order; Go maps are unordered.
//     So every object is built as an *OMap (insertion-ordered).
//   - ensure_ascii=True: non-ASCII runes are emitted as \uXXXX escapes.
//   - floats render via Python's float repr (see pyFloat), ints as ints.

// OMap is an insertion-ordered JSON object. The reasoners build their context
// payloads with a fixed key order, so callers construct these with omap(...) and
// the encoder preserves that order — matching Python dict repr / json.dumps.
type OMap struct {
	keys []string
	vals map[string]any
}

// omap builds an ordered object from alternating key, value arguments.
func omap(kv ...any) *OMap {
	m := &OMap{vals: make(map[string]any, len(kv)/2)}
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		if _, ok := m.vals[k]; !ok {
			m.keys = append(m.keys, k)
		}
		m.vals[k] = kv[i+1]
	}
	return m
}

// Set appends or overwrites a key, preserving first-insertion order.
func (m *OMap) Set(k string, v any) *OMap {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
	return m
}

// GetStr returns the string value at key, or def if absent/not a string —
// mirroring Python dict.get(key, default) followed by use as a string.
func (m *OMap) GetStr(key, def string) string {
	if v, ok := m.vals[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// GetStrSlice returns the []string value at key, or an empty slice if absent.
func (m *OMap) GetStrSlice(key string) []string {
	if v, ok := m.vals[key]; ok {
		if s, ok := v.([]string); ok {
			return s
		}
	}
	return nil
}

// Has reports whether key is present (mirrors `key in dict`).
func (m *OMap) Has(key string) bool {
	_, ok := m.vals[key]
	return ok
}

// pyJSON reproduces json.dumps(v, default=str) for the value kinds our contexts
// use: *OMap, []string, []any, string, int, float64, bool, nil, *string.
func pyJSON(v any) string {
	var b strings.Builder
	pyJSONWrite(&b, v)
	return b.String()
}

func pyJSONWrite(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case *OMap:
		if x == nil {
			b.WriteString("null")
			return
		}
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			pyJSONString(b, k)
			b.WriteString(": ")
			pyJSONWrite(b, x.vals[k])
		}
		b.WriteByte('}')
	case string:
		pyJSONString(b, x)
	case *string:
		if x == nil {
			b.WriteString("null")
		} else {
			pyJSONString(b, *x)
		}
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(pyFloat(x))
	case float32:
		b.WriteString(pyFloat(float64(x)))
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyJSONString(b, e)
		}
		b.WriteByte(']')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyJSONWrite(b, e)
		}
		b.WriteByte(']')
	case []*OMap:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyJSONWrite(b, e)
		}
		b.WriteByte(']')
	default:
		// json.dumps default=str: fall back to str(obj). Our payloads never hit
		// this, but keep it faithful rather than panicking.
		pyJSONString(b, pyStr(v))
	}
}

// pyJSONString reproduces json.dumps of a string with ensure_ascii=True: the
// standard short escapes plus \uXXXX for every control char and every non-ASCII
// rune (astral runes as UTF-16 surrogate pairs). Note Python does NOT escape '/'.
func pyJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				writeU(b, r)
			case r < 0x7f:
				b.WriteRune(r)
			case r <= 0xffff:
				writeU(b, r)
			default:
				// Astral plane -> UTF-16 surrogate pair, as CPython emits.
				r -= 0x10000
				hi := 0xd800 + (r >> 10)
				lo := 0xdc00 + (r & 0x3ff)
				writeU(b, hi)
				writeU(b, lo)
			}
		}
	}
	b.WriteByte('"')
}

const hexDigits = "0123456789abcdef"

func writeU(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[(r>>12)&0xf])
	b.WriteByte(hexDigits[(r>>8)&0xf])
	b.WriteByte(hexDigits[(r>>4)&0xf])
	b.WriteByte(hexDigits[r&0xf])
}
