package hitl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// sortedAnyKeys returns the sorted key set of a map[string]any for exact
// key-set assertions on the hand-built (camelCase) hax payload.
func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// selectedIDs collapses the decision's selected-id set to a sorted slice for
// comparison.
func selectedIDs(d ReviewDecision) []string {
	out := make([]string, 0, len(d.SelectedFindingIDs))
	for id := range d.SelectedFindingIDs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// --- V5: decision state machine ------------------------------------------

func TestParseReviewDecision(t *testing.T) {
	allIDs := []string{"f_001", "f_002", "f_003"}

	tests := []struct {
		name         string
		res          *agent.ApprovalResult
		wantAction   string
		wantInstr    string
		wantRaw      string
		wantSelected []string // nil => don't assert
	}{
		{
			name:       "expired -> reject",
			res:        &agent.ApprovalResult{Decision: "expired", Feedback: "timed out"},
			wantAction: ActionReject, wantInstr: "timed out", wantRaw: "expired",
		},
		{
			name:       "error -> reject",
			res:        &agent.ApprovalResult{Decision: "error", Feedback: "boom"},
			wantAction: ActionReject, wantInstr: "boom", wantRaw: "error",
		},
		{
			name:       "rejected with no values -> reject",
			res:        &agent.ApprovalResult{Decision: "rejected", Feedback: "no thanks"},
			wantAction: ActionReject, wantInstr: "no thanks", wantRaw: "rejected",
		},
		{
			name:       "rejected with empty values map -> reject",
			res:        &agent.ApprovalResult{Decision: "rejected", Feedback: "fb", RawResponse: map[string]any{"values": map[string]any{}}},
			wantAction: ActionReject, wantInstr: "fb", wantRaw: "rejected",
		},
		{
			name: "post honors findings_to_post subset",
			res: &agent.ApprovalResult{Decision: "approved", RawResponse: map[string]any{
				"values": map[string]any{"action": "post_selected", "findings_to_post": []any{"f_001", "f_003"}},
			}},
			wantAction: ActionPost, wantRaw: "approved", wantSelected: []string{"f_001", "f_003"},
		},
		{
			name: "post with absent findings_to_post -> all ids",
			res: &agent.ApprovalResult{Decision: "approved", RawResponse: map[string]any{
				"values": map[string]any{"action": "post_selected"},
			}},
			wantAction: ActionPost, wantRaw: "approved", wantSelected: []string{"f_001", "f_002", "f_003"},
		},
		{
			name: "rerun carries instructions",
			res: &agent.ApprovalResult{Decision: "approved", RawResponse: map[string]any{
				"values": map[string]any{"action": "rerun", "instructions": "tone it down"},
			}},
			wantAction: ActionRerun, wantInstr: "tone it down", wantRaw: "approved",
		},
		{
			name: "rerun instructions fall back to feedback",
			res: &agent.ApprovalResult{Decision: "approved", Feedback: "less aggressive please", RawResponse: map[string]any{
				"values": map[string]any{"action": "rerun"},
			}},
			wantAction: ActionRerun, wantInstr: "less aggressive please", wantRaw: "approved",
		},
		{
			name: "values nested under response.values",
			res: &agent.ApprovalResult{Decision: "approved", RawResponse: map[string]any{
				"response": map[string]any{"values": map[string]any{"action": "rerun", "instructions": "nested"}},
			}},
			wantAction: ActionRerun, wantInstr: "nested", wantRaw: "approved",
		},
		{
			name: "rejected but values present with post action -> post subset",
			res: &agent.ApprovalResult{Decision: "rejected", RawResponse: map[string]any{
				"values": map[string]any{"action": "post_selected", "findings_to_post": []any{"f_002"}},
			}},
			wantAction: ActionPost, wantRaw: "rejected", wantSelected: []string{"f_002"},
		},
		{
			name: "invalid action + approved -> post all",
			res: &agent.ApprovalResult{Decision: "approved", RawResponse: map[string]any{
				"values": map[string]any{"action": "bogus"},
			}},
			wantAction: ActionPost, wantRaw: "approved", wantSelected: []string{"f_001", "f_002", "f_003"},
		},
		{
			name: "invalid action + rejected (values present) -> reject",
			res: &agent.ApprovalResult{Decision: "rejected", RawResponse: map[string]any{
				"values": map[string]any{"action": "bogus", "instructions": "x"},
			}},
			wantAction: ActionReject, wantInstr: "x", wantRaw: "rejected",
		},
		{
			name:       "nil approval -> post all (empty approval, not terminal)",
			res:        nil,
			wantAction: ActionPost, wantRaw: "", wantSelected: []string{"f_001", "f_002", "f_003"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseReviewDecision(tc.res, allIDs)
			if got.Action != tc.wantAction {
				t.Errorf("Action = %q, want %q", got.Action, tc.wantAction)
			}
			if got.DecisionRaw != tc.wantRaw {
				t.Errorf("DecisionRaw = %q, want %q", got.DecisionRaw, tc.wantRaw)
			}
			if tc.wantInstr != "" && got.Instructions != tc.wantInstr {
				t.Errorf("Instructions = %q, want %q", got.Instructions, tc.wantInstr)
			}
			if tc.wantSelected != nil {
				if diff := selectedIDs(got); !reflect.DeepEqual(diff, tc.wantSelected) {
					t.Errorf("SelectedFindingIDs = %v, want %v", diff, tc.wantSelected)
				}
			}
		})
	}
}

// TestReviewDecisionPredicates confirms the Is* predicates track Action.
func TestReviewDecisionPredicates(t *testing.T) {
	if !(ReviewDecision{Action: ActionPost}).IsPost() {
		t.Error("IsPost")
	}
	if !(ReviewDecision{Action: ActionRerun}).IsRerun() {
		t.Error("IsRerun")
	}
	if !(ReviewDecision{Action: ActionReject}).IsReject() {
		t.Error("IsReject")
	}
	if (ReviewDecision{Action: ActionPost}).IsReject() {
		t.Error("post must not report IsReject")
	}
}

// --- V5: payload shape + severity re-normalization -----------------------

func TestBuildReviewPayloadShape(t *testing.T) {
	sug := "use parameterized queries"
	findings := []schemas.ScoredFinding{
		{
			ID: "f_001", Severity: "important", Title: "SQL injection",
			FilePath: "db/query.go", LineStart: 10, LineEnd: 12,
			Body: "concatenated user input", Suggestion: &sug,
			DimensionName: "Security", Confidence: 0.9,
		},
	}
	payload, err := BuildReviewPayload("PR body intent", findings, "PR-AF Review Approval", nil, 0, nil)
	if err != nil {
		t.Fatalf("BuildReviewPayload: %v", err)
	}

	wantTop := []string{
		"findings", "instructionsPlaceholder", "intent",
		"postLabel", "rejectLabel", "rerunLabel", "reviewSummary", "title",
	}
	if got := sortedAnyKeys(payload); !reflect.DeepEqual(got, wantTop) {
		t.Errorf("top-level keys = %v, want %v", got, wantTop)
	}

	if payload["title"] != "PR-AF Review Approval" {
		t.Errorf("title = %v", payload["title"])
	}
	if payload["postLabel"] != "Post selected" ||
		payload["rerunLabel"] != "Re-review with instructions" ||
		payload["rejectLabel"] != "Reject" {
		t.Errorf("labels wrong: %v / %v / %v", payload["postLabel"], payload["rerunLabel"], payload["rejectLabel"])
	}
	if payload["instructionsPlaceholder"] != "e.g. too aggressive, tone it down and drop the nitpicks" {
		t.Errorf("instructionsPlaceholder = %v", payload["instructionsPlaceholder"])
	}
	if payload["reviewSummary"] != "PR-AF found 1 finding(s) (1 important)." {
		t.Errorf("reviewSummary = %v", payload["reviewSummary"])
	}

	entries, ok := payload["findings"].([]map[string]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("findings = %#v", payload["findings"])
	}
	fp := entries[0]
	wantFP := []string{
		"body", "confidence", "defaultSelected", "dimension",
		"filePath", "id", "lineEnd", "lineStart", "severity", "suggestion", "title",
	}
	if got := sortedAnyKeys(fp); !reflect.DeepEqual(got, wantFP) {
		t.Errorf("finding entry keys = %v, want %v", got, wantFP)
	}
	if fp["defaultSelected"] != true {
		t.Errorf("defaultSelected = %v, want true", fp["defaultSelected"])
	}
	if fp["id"] != "f_001" || fp["title"] != "SQL injection" || fp["severity"] != "important" {
		t.Errorf("finding scalar fields wrong: %#v", fp)
	}
	if fp["filePath"] != "db/query.go" || fp["lineStart"] != 10 || fp["lineEnd"] != 12 {
		t.Errorf("finding location fields wrong: %#v", fp)
	}
	if fp["suggestion"] != "use parameterized queries" || fp["dimension"] != "Security" {
		t.Errorf("finding suggestion/dimension wrong: %#v", fp)
	}
	if fp["confidence"] != 0.9 {
		t.Errorf("confidence = %v, want 0.9", fp["confidence"])
	}
}

// TestBuildReviewPayloadMinimalFinding asserts optional keys are omitted when
// their source field is empty/zero — only the four always-present keys plus the
// always-emitted confidence remain.
func TestBuildReviewPayloadMinimalFinding(t *testing.T) {
	findings := []schemas.ScoredFinding{
		{ID: "f_001", Severity: "nitpick", Title: "style", Confidence: 0.5},
	}
	payload, err := BuildReviewPayload("", findings, "T", nil, 0, nil)
	if err != nil {
		t.Fatalf("BuildReviewPayload: %v", err)
	}
	fp := payload["findings"].([]map[string]any)[0]
	want := []string{"confidence", "defaultSelected", "id", "severity", "title"}
	if got := sortedAnyKeys(fp); !reflect.DeepEqual(got, want) {
		t.Errorf("minimal finding keys = %v, want %v", got, want)
	}
	// Empty intent stays "".
	if payload["intent"] != "" {
		t.Errorf("intent = %q, want empty", payload["intent"])
	}
}

// TestFindingPayloadSeverityRenormalization is the zod-enum-422 defense: a
// finding carrying a stray "high" (constructed by struct literal, so it never
// passed through the unmarshal coercion) must be re-normalized to "important"
// before it reaches the pr-af-review-v1 template.
func TestFindingPayloadSeverityRenormalization(t *testing.T) {
	findings := []schemas.ScoredFinding{
		{ID: "f_001", Severity: "high", Title: "x", Confidence: 0.5},
	}
	payload, err := BuildReviewPayload("", findings, "T", nil, 0, nil)
	if err != nil {
		t.Fatalf("BuildReviewPayload: %v", err)
	}
	fp := payload["findings"].([]map[string]any)[0]
	if fp["severity"] != "important" {
		t.Errorf("severity = %v, want important (high re-normalized)", fp["severity"])
	}
}

// TestBuildReviewPayloadNoFindings covers the "no findings" review summary and
// the empty (non-nil) findings slice.
func TestBuildReviewPayloadNoFindings(t *testing.T) {
	payload, err := BuildReviewPayload("intent", nil, "T", nil, 0, nil)
	if err != nil {
		t.Fatalf("BuildReviewPayload: %v", err)
	}
	if payload["reviewSummary"] != "PR-AF found 0 finding(s) (no findings)." {
		t.Errorf("reviewSummary = %v", payload["reviewSummary"])
	}
	entries, ok := payload["findings"].([]map[string]any)
	if !ok || entries == nil || len(entries) != 0 {
		t.Errorf("findings = %#v, want empty non-nil slice", payload["findings"])
	}
}

// TestBuildReviewPayloadOptionalSections covers the pr{} and revision{} blocks:
// present when populated, absent otherwise, empty pr-meta values dropped, and
// blank prior instructions filtered.
func TestBuildReviewPayloadOptionalSections(t *testing.T) {
	t.Run("absent by default", func(t *testing.T) {
		payload, _ := BuildReviewPayload("i", nil, "T", nil, 0, nil)
		if _, ok := payload["pr"]; ok {
			t.Error("pr must be absent")
		}
		if _, ok := payload["revision"]; ok {
			t.Error("revision must be absent")
		}
	})

	t.Run("pr meta drops empties", func(t *testing.T) {
		payload, _ := BuildReviewPayload("i", nil, "T", map[string]any{
			"title": "My PR", "number": 42, "author": "", "url": nil,
		}, 0, nil)
		pr, ok := payload["pr"].(map[string]any)
		if !ok {
			t.Fatalf("pr = %#v", payload["pr"])
		}
		want := []string{"number", "title"}
		if got := sortedAnyKeys(pr); !reflect.DeepEqual(got, want) {
			t.Errorf("pr keys = %v, want %v", got, want)
		}
	})

	t.Run("pr meta all-empty -> absent", func(t *testing.T) {
		payload, _ := BuildReviewPayload("i", nil, "T", map[string]any{"a": "", "b": nil}, 0, nil)
		if _, ok := payload["pr"]; ok {
			t.Error("pr must be absent when all values empty")
		}
	})

	t.Run("revision present with filtered instructions", func(t *testing.T) {
		payload, _ := BuildReviewPayload("i", nil, "T", nil, 2, []string{"first", "  ", "", "second"})
		rev, ok := payload["revision"].(map[string]any)
		if !ok {
			t.Fatalf("revision = %#v", payload["revision"])
		}
		if rev["iteration"] != 2 {
			t.Errorf("iteration = %v, want 2", rev["iteration"])
		}
		prior, ok := rev["priorInstructions"].([]string)
		if !ok || !reflect.DeepEqual(prior, []string{"first", "second"}) {
			t.Errorf("priorInstructions = %#v, want [first second]", rev["priorInstructions"])
		}
	})

	t.Run("revision from iteration alone yields empty prior list", func(t *testing.T) {
		payload, _ := BuildReviewPayload("i", nil, "T", nil, 1, nil)
		rev := payload["revision"].(map[string]any)
		prior, ok := rev["priorInstructions"].([]string)
		if !ok || prior == nil || len(prior) != 0 {
			t.Errorf("priorInstructions = %#v, want empty non-nil slice", rev["priorInstructions"])
		}
	})
}

// --- V5: three failure paths -> reject with exact instruction strings ----

func TestRequestReviewApprovalPayloadBuildFailure(t *testing.T) {
	orig := buildPayloadFn
	buildPayloadFn = func(string, []schemas.ScoredFinding, string, map[string]any, int, []string) (map[string]any, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { buildPayloadFn = orig })

	dec := RequestReviewApproval(context.Background(), RequestReviewApprovalArgs{
		// HaxClient/Pauser intentionally nil: the build failure returns before
		// either is touched.
		Findings:       []schemas.ScoredFinding{{ID: "f_001"}},
		ExpiresInHours: 72,
	})
	if !dec.IsReject() {
		t.Fatalf("action = %q, want reject", dec.Action)
	}
	if dec.Instructions != "payload build failed: boom" {
		t.Errorf("instructions = %q, want %q", dec.Instructions, "payload build failed: boom")
	}
}

func TestRequestReviewApprovalCreateRequestFailure(t *testing.T) {
	clearEnv(t, haxEnvKeys...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"hax down"}`))
	}))
	defer srv.Close()

	dec := RequestReviewApproval(context.Background(), RequestReviewApprovalArgs{
		HaxClient:      &HaxClient{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()},
		Pauser:         &fakePauser{}, // must never be reached
		Findings:       []schemas.ScoredFinding{{ID: "f_001"}},
		ExpiresInHours: 72,
	})
	if !dec.IsReject() {
		t.Fatalf("action = %q, want reject", dec.Action)
	}
	if !strings.HasPrefix(dec.Instructions, "create_request failed:") {
		t.Errorf("instructions = %q, want prefix %q", dec.Instructions, "create_request failed:")
	}
	if !strings.Contains(dec.Instructions, "status 500") {
		t.Errorf("instructions = %q, want it to mention status 500", dec.Instructions)
	}
}

func TestRequestReviewApprovalPauseFailure(t *testing.T) {
	clearEnv(t, haxEnvKeys...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"req_1","url":"https://hax.example/r/req_1"}`))
	}))
	defer srv.Close()

	fp := &fakePauser{err: errors.New("pause boom")}
	dec := RequestReviewApproval(context.Background(), RequestReviewApprovalArgs{
		HaxClient:      &HaxClient{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()},
		Pauser:         fp,
		Findings:       []schemas.ScoredFinding{{ID: "f_001"}},
		ExpiresInHours: 72,
	})
	if !dec.IsReject() {
		t.Fatalf("action = %q, want reject", dec.Action)
	}
	if dec.Instructions != "pause failed: pause boom" {
		t.Errorf("instructions = %q, want %q", dec.Instructions, "pause failed: pause boom")
	}
	// The create-request must have succeeded and the resolved request id/url must
	// have been forwarded to Pause.
	if fp.gotOpts.ApprovalRequestID != "req_1" || fp.gotOpts.ExpiresInHours != 72 {
		t.Errorf("pause opts = %+v, want id=req_1 hours=72", fp.gotOpts)
	}
}

// TestRequestReviewApprovalHappyPath drives a full round: create succeeds, the
// human approves a subset, and the decision reflects it.
func TestRequestReviewApprovalHappyPath(t *testing.T) {
	clearEnv(t, haxEnvKeys...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"req_9","url":"u"}`))
	}))
	defer srv.Close()

	fp := &fakePauser{result: &agent.ApprovalResult{
		Decision: "approved",
		RawResponse: map[string]any{
			"values": map[string]any{"action": "post_selected", "findings_to_post": []any{"f_002"}},
		},
	}}
	dec := RequestReviewApproval(context.Background(), RequestReviewApprovalArgs{
		HaxClient:      &HaxClient{BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()},
		Pauser:         fp,
		PRLabel:        "owner/repo#1",
		Findings:       []schemas.ScoredFinding{{ID: "f_001"}, {ID: "f_002"}},
		ExpiresInHours: 48,
	})
	if !dec.IsPost() {
		t.Fatalf("action = %q, want post_selected", dec.Action)
	}
	if got := selectedIDs(dec); !reflect.DeepEqual(got, []string{"f_002"}) {
		t.Errorf("selected = %v, want [f_002]", got)
	}
	if fp.gotOpts.ApprovalRequestID != "req_9" || fp.gotOpts.ExpiresInHours != 48 {
		t.Errorf("pause opts = %+v", fp.gotOpts)
	}
}

// --- V5: clean_intent table ----------------------------------------------

func TestCleanIntent(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxChars int
		want     string
	}{
		{name: "empty", in: "", maxChars: MaxIntentChars, want: ""},
		{
			name:     "html stripped and whitespace collapsed",
			in:       "<p>Hello</p> <b>world</b>",
			maxChars: MaxIntentChars,
			want:     "Hello world",
		},
		{
			name:     "tabs collapsed, blank-line runs squeezed, per-line trimmed",
			in:       "line1\t\t  \nline2\n\n\n\nline3",
			maxChars: MaxIntentChars,
			want:     "line1\nline2\n\nline3",
		},
		{
			name:     "truncated at maxChars with ellipsis",
			in:       strings.Repeat("a", 800),
			maxChars: MaxIntentChars,
			want:     strings.Repeat("a", MaxIntentChars) + "…",
		},
		{
			name:     "truncation rstrips a trailing space before the ellipsis",
			in:       strings.Repeat("a", 699) + "   bbbb", // ws collapses -> 699 a's, space, bbbb
			maxChars: MaxIntentChars,
			want:     strings.Repeat("a", 699) + "…",
		},
		{
			name:     "exactly maxChars is not truncated",
			in:       strings.Repeat("a", MaxIntentChars),
			maxChars: MaxIntentChars,
			want:     strings.Repeat("a", MaxIntentChars),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanIntent(tc.in, tc.maxChars); got != tc.want {
				t.Errorf("CleanIntent(%q, %d) = %q, want %q", tc.in, tc.maxChars, got, tc.want)
			}
		})
	}
}
