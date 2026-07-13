package gates

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Agent-Field/agentfield/sdk/go/ai"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// textResponse builds an *ai.Response whose Text() returns s, matching the
// shape the SDK produces (first choice, one text content part).
func textResponse(s string) *ai.Response {
	return &ai.Response{
		Choices: []ai.Choice{
			{Message: ai.Message{Content: []ai.ContentPart{{Type: "text", Text: s}}}},
		},
	}
}

// fakeAI is a test double for the AICaller seam. fn maps each prompt to a
// response/error; calls counts every invocation (thread-safe).
type fakeAI struct {
	mu    sync.Mutex
	calls int
	fn    func(prompt string) (*ai.Response, error)
}

func (f *fakeAI) AI(_ context.Context, prompt string, _ ...ai.Option) (*ai.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(prompt)
}

func (f *fakeAI) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// -------------------- parseVerdict (contract V6) --------------------

func TestParseVerdictCleanJSON(t *testing.T) {
	v := parseVerdict(`{"blocking": true, "reason": "breaks the build"}`)
	if !v.blocking {
		t.Fatalf("expected blocking=true, got false")
	}
	if v.reason != "breaks the build" {
		t.Fatalf("reason = %q, want %q", v.reason, "breaks the build")
	}
}

func TestParseVerdictBlockingFalse(t *testing.T) {
	v := parseVerdict(`{"blocking": false, "reason": "style only"}`)
	if v.blocking {
		t.Fatalf("expected blocking=false")
	}
	if v.reason != "style only" {
		t.Fatalf("reason = %q", v.reason)
	}
}

func TestParseVerdictFencedJSON(t *testing.T) {
	raw := "```json\n{\"blocking\": true, \"reason\": \"regression\"}\n```"
	v := parseVerdict(raw)
	if !v.blocking || v.reason != "regression" {
		t.Fatalf("fenced json parse failed: %+v", v)
	}
}

func TestParseVerdictFencedNoLang(t *testing.T) {
	raw := "```\n{\"blocking\": false, \"reason\": \"ok\"}\n```"
	v := parseVerdict(raw)
	if v.blocking || v.reason != "ok" {
		t.Fatalf("fenced (no lang) parse failed: %+v", v)
	}
}

func TestParseVerdictEmbeddedInProse(t *testing.T) {
	raw := `Here is my verdict: {"blocking": true, "reason": "auth bypass"} — thanks!`
	v := parseVerdict(raw)
	if !v.blocking || v.reason != "auth bypass" {
		t.Fatalf("prose-embedded parse failed: %+v", v)
	}
}

func TestParseVerdictNonObjectArray(t *testing.T) {
	v := parseVerdict(`[1, 2, 3]`)
	if v.blocking {
		t.Fatalf("non-object must be blocking=false")
	}
	if v.reason != "gate non-object" {
		t.Fatalf("reason = %q, want %q", v.reason, "gate non-object")
	}
}

func TestParseVerdictNonObjectScalars(t *testing.T) {
	// Valid JSON that is not an object → "gate non-object".
	for _, raw := range []string{`42`, `"hi"`, `null`, `true`} {
		v := parseVerdict(raw)
		if v.blocking || v.reason != "gate non-object" {
			t.Fatalf("raw %q: got %+v, want non-object", raw, v)
		}
	}
}

func TestParseVerdictGarbage(t *testing.T) {
	for _, raw := range []string{`not json at all`, `{broken`, ``, `   `} {
		v := parseVerdict(raw)
		if v.blocking {
			t.Fatalf("raw %q: blocking must be false", raw)
		}
		if v.reason != "gate parse error" {
			t.Fatalf("raw %q: reason = %q, want %q", raw, v.reason, "gate parse error")
		}
	}
}

func TestParseVerdictReasonTruncatedTo400(t *testing.T) {
	long := strings.Repeat("x", 500)
	v := parseVerdict(fmt.Sprintf(`{"blocking": true, "reason": %q}`, long))
	if got := len([]rune(v.reason)); got != 400 {
		t.Fatalf("reason length = %d, want 400", got)
	}
	if v.reason != strings.Repeat("x", 400) {
		t.Fatalf("reason not truncated to first 400 chars")
	}
}

func TestParseVerdictReasonTruncatedByRune(t *testing.T) {
	// 500 multi-byte runes must truncate to 400 runes (not 400 bytes).
	long := strings.Repeat("é", 500)
	v := parseVerdict(fmt.Sprintf(`{"blocking": false, "reason": %q}`, long))
	if got := len([]rune(v.reason)); got != 400 {
		t.Fatalf("rune-truncated reason length = %d, want 400", got)
	}
}

func TestParseVerdictMissingReasonDefaultsEmpty(t *testing.T) {
	v := parseVerdict(`{"blocking": true}`)
	if !v.blocking {
		t.Fatalf("expected blocking=true")
	}
	if v.reason != "" {
		t.Fatalf("absent reason must default to empty, got %q", v.reason)
	}
}

func TestParseVerdictBlockingTruthiness(t *testing.T) {
	// Python bool() coercion over non-bool JSON values.
	cases := map[string]bool{
		`{"blocking": 1}`:     true,
		`{"blocking": 0}`:     false,
		`{"blocking": "yes"}`: true,
		`{"blocking": ""}`:    false,
		`{"blocking": null}`:  false,
		`{}`:                  false,
	}
	for raw, want := range cases {
		if got := parseVerdict(raw).blocking; got != want {
			t.Fatalf("raw %q: blocking = %v, want %v", raw, got, want)
		}
	}
}

// -------------------- ClassifyFindings (contract V6) --------------------

func scored(id, title string) schemas.ScoredFinding {
	return schemas.ScoredFinding{ID: id, Title: title, Severity: "important", Confidence: 0.5}
}

func TestClassifyEmptyIsPassthroughZeroCalls(t *testing.T) {
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		t.Fatalf("AI must not be called for empty findings")
		return nil, nil
	}}
	out := ClassifyFindings(context.Background(), f, nil)
	if len(out) != 0 {
		t.Fatalf("expected empty passthrough, got %d", len(out))
	}
	if f.callCount() != 0 {
		t.Fatalf("expected zero AI calls, got %d", f.callCount())
	}
}

func TestClassifyOneCallPerFinding(t *testing.T) {
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		return textResponse(`{"blocking": false, "reason": "ok"}`), nil
	}}
	findings := []schemas.ScoredFinding{scored("f1", "a"), scored("f2", "b"), scored("f3", "c")}
	out := ClassifyFindings(context.Background(), f, findings)
	if f.callCount() != 3 {
		t.Fatalf("expected exactly 3 AI calls, got %d", f.callCount())
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 findings out, got %d", len(out))
	}
}

func TestClassifyAIFailureIsAdvisory(t *testing.T) {
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		return nil, errors.New("boom")
	}}
	out := ClassifyFindings(context.Background(), f, []schemas.ScoredFinding{scored("f1", "a")})
	if out[0].Blocking {
		t.Fatalf("AI failure must produce blocking=false (advisory)")
	}
	if out[0].BlockingReason != "gate error" {
		t.Fatalf("BlockingReason = %q, want %q", out[0].BlockingReason, "gate error")
	}
}

func TestClassifyOrderPreservedAndInputNotMutated(t *testing.T) {
	// Each finding titled "T<i>"; the fake echoes the index back as the reason
	// and blocks even indices. Correct positional alignment is only possible if
	// results land at results[i] regardless of goroutine completion order.
	const n = 12
	findings := make([]schemas.ScoredFinding, n)
	for i := 0; i < n; i++ {
		findings[i] = scored(fmt.Sprintf("f%d", i), fmt.Sprintf("T%d", i))
	}
	f := &fakeAI{fn: func(prompt string) (*ai.Response, error) {
		idx := indexFromPrompt(t, prompt)
		blocking := idx%2 == 0
		return textResponse(fmt.Sprintf(`{"blocking": %t, "reason": "r%d"}`, blocking, idx)), nil
	}}
	out := ClassifyFindings(context.Background(), f, findings)
	for i := 0; i < n; i++ {
		wantReason := fmt.Sprintf("r%d", i)
		if out[i].BlockingReason != wantReason {
			t.Fatalf("out[%d].BlockingReason = %q, want %q", i, out[i].BlockingReason, wantReason)
		}
		if want := i%2 == 0; out[i].Blocking != want {
			t.Fatalf("out[%d].Blocking = %v, want %v", i, out[i].Blocking, want)
		}
		// Input must be untouched (pure function).
		if findings[i].Blocking || findings[i].BlockingReason != "" {
			t.Fatalf("input finding %d was mutated: %+v", i, findings[i])
		}
	}
}

// indexFromPrompt extracts the integer N from the "Title: TN" line the
// merge-gate user prompt embeds.
func indexFromPrompt(t *testing.T, prompt string) int {
	t.Helper()
	marker := "Title: T"
	i := strings.Index(prompt, marker)
	if i < 0 {
		t.Fatalf("prompt missing %q marker: %q", marker, prompt)
	}
	rest := prompt[i+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	var n int
	if _, err := fmt.Sscanf(rest, "%d", &n); err != nil {
		t.Fatalf("cannot parse index from %q: %v", rest, err)
	}
	return n
}

// -------------------- PolishComments (contract V6) --------------------

func comment(path, body string) schemas.GitHubComment {
	return schemas.GitHubComment{Path: path, Line: 10, Side: "RIGHT", Body: body}
}

func TestPolishEmptyIsPassthroughZeroCalls(t *testing.T) {
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		t.Fatalf("AI must not be called for empty comments")
		return nil, nil
	}}
	out := PolishComments(context.Background(), f, nil)
	if len(out) != 0 {
		t.Fatalf("expected empty passthrough, got %d", len(out))
	}
	if f.callCount() != 0 {
		t.Fatalf("expected zero AI calls, got %d", f.callCount())
	}
}

func TestPolishRewritesBodyPreservesPathAndFields(t *testing.T) {
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		return textResponse("polished text"), nil
	}}
	in := []schemas.GitHubComment{comment("src/a.go", "original body")}
	out := PolishComments(context.Background(), f, in)
	if out[0].Body != "polished text" {
		t.Fatalf("body not rewritten: %q", out[0].Body)
	}
	if out[0].Path != "src/a.go" || out[0].Line != 10 || out[0].Side != "RIGHT" {
		t.Fatalf("non-body fields not preserved: %+v", out[0])
	}
	// Input not mutated.
	if in[0].Body != "original body" {
		t.Fatalf("input comment mutated: %q", in[0].Body)
	}
}

func TestPolishFailureKeepsOriginalBody(t *testing.T) {
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		return nil, errors.New("boom")
	}}
	out := PolishComments(context.Background(), f, []schemas.GitHubComment{comment("p", "keep me")})
	if out[0].Body != "keep me" {
		t.Fatalf("failure must keep original body, got %q", out[0].Body)
	}
}

func TestPolishEmptyRewriteKeepsOriginalBody(t *testing.T) {
	// Whitespace-only rewrite → strip to "" → keep original (Python `text or body`).
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		return textResponse("   \n  "), nil
	}}
	out := PolishComments(context.Background(), f, []schemas.GitHubComment{comment("p", "keep me too")})
	if out[0].Body != "keep me too" {
		t.Fatalf("empty rewrite must keep original body, got %q", out[0].Body)
	}
}

func TestPolishPreservesCalloutVerbatim(t *testing.T) {
	// The polish prompt instructs the model to preserve callouts; a faithful
	// rewrite that echoes the callout must survive intact through the code path.
	body := "> [!CAUTION] **Must-fix before merge.**\n\nFix the null deref."
	f := &fakeAI{fn: func(string) (*ai.Response, error) {
		return textResponse(body), nil
	}}
	out := PolishComments(context.Background(), f, []schemas.GitHubComment{comment("p", body)})
	if !strings.HasPrefix(out[0].Body, "> [!CAUTION] **Must-fix before merge.**") {
		t.Fatalf("callout not preserved: %q", out[0].Body)
	}
}

func TestPolishOrderPreserved(t *testing.T) {
	const n = 10
	in := make([]schemas.GitHubComment, n)
	for i := 0; i < n; i++ {
		in[i] = comment(fmt.Sprintf("p%d", i), fmt.Sprintf("body-%d", i))
	}
	f := &fakeAI{fn: func(prompt string) (*ai.Response, error) {
		// Echo the original body suffixed, so alignment is checkable.
		marker := "body-"
		i := strings.Index(prompt, marker)
		if i < 0 {
			t.Fatalf("prompt missing body marker: %q", prompt)
		}
		return textResponse(prompt[i:] + "|polished"), nil
	}}
	out := PolishComments(context.Background(), f, in)
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("body-%d|polished", i)
		if out[i].Body != want {
			t.Fatalf("out[%d].Body = %q, want %q", i, out[i].Body, want)
		}
	}
}
