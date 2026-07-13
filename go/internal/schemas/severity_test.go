package schemas

import (
	"encoding/json"
	"testing"
)

// TestNormalizeSeverityCanonical is the canonical half of validation contract
// V3: the schemas/severity.py alias map. (The divergent scoring map —
// medium->important, low->suggestion — is tested in the scoring package.) Cases
// are derived from the contract, not the implementation: each label maps by
// rank, case/space-insensitively, with junk / non-string falling back to
// "suggestion".
func TestNormalizeSeverityCanonical(t *testing.T) {
	cases := []struct {
		in   any
		want Severity
	}{
		// identity
		{"critical", "critical"},
		{"important", "important"},
		{"suggestion", "suggestion"},
		{"nitpick", "nitpick"},
		// critical-tier synonyms
		{"blocker", "critical"},
		{"fatal", "critical"},
		{"severe", "critical"},
		// important-tier synonyms (the "high" that caused the incident)
		{"high", "important"},
		{"error", "important"},
		{"major", "important"},
		// suggestion-tier synonyms — DIVERGES from the scoring map on purpose
		{"medium", "suggestion"},
		{"moderate", "suggestion"},
		{"warning", "suggestion"},
		{"warn", "suggestion"},
		// nitpick-tier synonyms
		{"low", "nitpick"},
		{"minor", "nitpick"},
		{"nit", "nitpick"},
		{"info", "nitpick"},
		{"informational", "nitpick"},
		{"trivial", "nitpick"},
		// case / whitespace insensitivity
		{"  HIGH  ", "important"},
		{"Medium", "suggestion"},
		{"LOW", "nitpick"},
		{"\tCritical\n", "critical"},
		// junk / empty -> default
		{"banana", "suggestion"},
		{"", "suggestion"},
		{"   ", "suggestion"},
		// non-string -> default
		{42, "suggestion"},
		{3.14, "suggestion"},
		{true, "suggestion"},
		{nil, "suggestion"},
		{[]string{"high"}, "suggestion"},
		// a Severity value is accepted like a string (Python str subclass)
		{Severity("blocker"), "critical"},
	}
	for _, c := range cases {
		if got := NormalizeSeverity(c.in, DefaultSeverity); got != c.want {
			t.Errorf("NormalizeSeverity(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeSeverityCustomDefault verifies the def argument is honored on
// unknown/non-string input.
func TestNormalizeSeverityCustomDefault(t *testing.T) {
	if got := NormalizeSeverity("banana", "critical"); got != "critical" {
		t.Errorf("NormalizeSeverity with custom default = %q, want %q", got, "critical")
	}
	if got := NormalizeSeverity(nil, "nitpick"); got != "nitpick" {
		t.Errorf("NormalizeSeverity(nil) custom default = %q, want %q", got, "nitpick")
	}
	// A known synonym ignores the custom default and maps by rank.
	if got := NormalizeSeverity("high", "nitpick"); got != "important" {
		t.Errorf("NormalizeSeverity(high) = %q, want %q", got, "important")
	}
}

// TestSeverityUnmarshalJSON asserts the Severity field type coerces stray labels
// (and null / non-string) through NormalizeSeverity on decode — the Go analog
// of the pydantic BeforeValidator.
func TestSeverityUnmarshalJSON(t *testing.T) {
	cases := []struct {
		json string
		want Severity
	}{
		{`"high"`, "important"},
		{`"critical"`, "critical"},
		{`"  Blocker "`, "critical"},
		{`"medium"`, "suggestion"},
		{`"garbage"`, "suggestion"},
		{`null`, "suggestion"},
		{`123`, "suggestion"},
		{`""`, "suggestion"},
	}
	for _, c := range cases {
		var s Severity
		if err := json.Unmarshal([]byte(c.json), &s); err != nil {
			t.Fatalf("unmarshal %s into Severity: %v", c.json, err)
		}
		if s != c.want {
			t.Errorf("Severity(%s) = %q, want %q", c.json, s, c.want)
		}
	}
}

// TestSeverityInFinding verifies a stray severity inside a struct is normalized
// on decode (not just standalone).
func TestSeverityInFinding(t *testing.T) {
	f := mustUnmarshal[ReviewFinding](t, `{"severity":"high"}`)
	if f.Severity != "important" {
		t.Errorf("ReviewFinding severity high -> %q, want important", f.Severity)
	}
	// Absent severity keeps the seeded default.
	f2 := mustUnmarshal[ReviewFinding](t, `{}`)
	if f2.Severity != "suggestion" {
		t.Errorf("ReviewFinding absent severity -> %q, want suggestion", f2.Severity)
	}
}
