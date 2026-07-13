package hitl

// review_gate.go is the PR-AF human-in-the-loop review gate — the Go port of
// src/pr_af/hitl/review_gate.py. It builds the "pr-af-review-v1" hax form
// (intent blurb + one entry per finding), creates the request via the hax
// substrate (hax_client.go), pauses the execution via the Pauser seam
// (pause.go), and maps the reviewer's response to a ReviewDecision:
//
//   - post_selected — post the checked subset of findings, or
//   - rerun         — re-run the review with free-text instructions, or
//   - reject         — post nothing.
//
// Every failure path (payload build, create-request, pause) is surfaced as a
// reject so the pipeline never posts an unreviewed review when the gate is on.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// HaxReviewTemplate is the hax template this gate renders into. Registered in
// hax-sdk under src/templates/pr-af-review (id "pr-af-review-v1"). Ports
// review_gate.py::HAX_REVIEW_TEMPLATE.
const HaxReviewTemplate = "pr-af-review-v1"

// The action the reviewer chose in the template's buttons. These string values
// are the contract with the hax template's response values.action. Port
// review_gate.py::{ACTION_POST,ACTION_RERUN,ACTION_REJECT}.
const (
	ActionPost   = "post_selected"
	ActionRerun  = "rerun"
	ActionReject = "reject"
)

// validActions is the _VALID_ACTIONS set from review_gate.py.
var validActions = map[string]struct{}{
	ActionPost:   {},
	ActionRerun:  {},
	ActionReject: {},
}

// MaxIntentChars is the longest PR-intent blurb shown in the form; the raw PR
// body is stripped of HTML and truncated to this. Ports
// review_gate.py::_MAX_INTENT_CHARS (700).
const MaxIntentChars = 700

// Regexes for CleanIntent, ported verbatim from review_gate.py.
var (
	htmlTagRE   = regexp.MustCompile(`<[^>]+>`) // _HTML_TAG_RE
	wsRE        = regexp.MustCompile(`[ \t]+`)  // _WS_RE
	blankLineRE = regexp.MustCompile(`\n{3,}`)  // _BLANKLINES_RE
)

// ReviewDecision is the parsed outcome of one HITL round. Ports
// review_gate.py::ReviewDecision (Python's set[str] -> map[string]struct{}).
type ReviewDecision struct {
	// Action is one of ActionPost | ActionRerun | ActionReject.
	Action string
	// SelectedFindingIDs is the set of finding ids to post (post_selected only).
	SelectedFindingIDs map[string]struct{}
	// Instructions is the reviewer's free-text (rerun) or the fallback feedback.
	Instructions string
	// DecisionRaw is the underlying agentfield decision string ("approved",
	// "expired", ...) kept for logs.
	DecisionRaw string
}

// IsPost reports whether the reviewer chose to post the selected findings.
func (d ReviewDecision) IsPost() bool { return d.Action == ActionPost }

// IsRerun reports whether the reviewer asked to re-run with instructions.
func (d ReviewDecision) IsRerun() bool { return d.Action == ActionRerun }

// IsReject reports whether the outcome is to post nothing.
func (d ReviewDecision) IsReject() bool { return d.Action == ActionReject }

// CleanIntent turns a raw PR body into a short, legible intent blurb: strip HTML
// tags, collapse whitespace, collapse blank-line runs, per-line trim, then
// truncate to maxChars codepoints with a trailing "…". Ports
// review_gate.py::clean_intent.
//
// Truncation is codepoint-aware (Python slices a unicode str; a byte slice would
// split multibyte runes), and the trailing trim uses unicode.IsSpace to mirror
// Python's str.rstrip().
func CleanIntent(text string, maxChars int) string {
	if text == "" {
		return ""
	}
	stripped := htmlTagRE.ReplaceAllString(text, " ")
	stripped = wsRE.ReplaceAllString(stripped, " ")
	stripped = blankLineRE.ReplaceAllString(stripped, "\n\n")
	lines := strings.Split(stripped, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	stripped = strings.Join(lines, "\n")
	stripped = strings.TrimSpace(stripped)
	runes := []rune(stripped)
	if len(runes) > maxChars {
		truncated := strings.TrimRightFunc(string(runes[:maxChars]), unicode.IsSpace)
		stripped = truncated + "…"
	}
	return stripped
}

// findingPayload builds one finding entry for the hax template. Keys are
// camelCase (per the template's zod schema) — this is one of the few camelCase
// surfaces in an otherwise snake_case system (design §B.5). Ports
// review_gate.py::_finding_payload.
//
// Severity is re-normalized here (defense-in-depth against the zod-enum 422): a
// finding built off the validated path could still carry a stray label like
// "high"; NormalizeSeverity coerces it to "important" before the create.
func findingPayload(f schemas.ScoredFinding) map[string]any {
	entry := map[string]any{
		"id":              f.ID,
		"severity":        string(schemas.NormalizeSeverity(f.Severity, schemas.DefaultSeverity)),
		"title":           f.Title,
		"defaultSelected": true,
	}
	if f.FilePath != "" {
		entry["filePath"] = f.FilePath
	}
	if f.LineStart > 0 {
		entry["lineStart"] = f.LineStart
	}
	if f.LineEnd > 0 {
		entry["lineEnd"] = f.LineEnd
	}
	if f.Body != "" {
		entry["body"] = f.Body
	}
	if f.Suggestion != nil && *f.Suggestion != "" {
		entry["suggestion"] = *f.Suggestion
	}
	if f.DimensionName != "" {
		entry["dimension"] = f.DimensionName
	}
	// Python: `if finding.confidence is not None`. Go ScoredFinding.Confidence is
	// a non-optional float64 (never nil), so it is always emitted.
	entry["confidence"] = f.Confidence
	return entry
}

// BuildReviewPayload builds the "pr-af-review-v1" request payload (camelCase,
// validated server-side against prAfReviewPayloadSchema). Ports
// review_gate.py::build_review_payload. The returned map is the hax payload; the
// error return exists so RequestReviewApproval can surface a build failure as a
// reject (the Python code wraps this call in try/except).
func BuildReviewPayload(
	prIntent string,
	findings []schemas.ScoredFinding,
	title string,
	prMeta map[string]any,
	revisionIter int,
	revisionHistory []string,
) (map[string]any, error) {
	// Severity counts, preserving first-seen order to match Python dict
	// insertion order (counts[f.severity] uses the finding's severity as-is).
	var order []string
	counts := map[string]int{}
	for _, f := range findings {
		sev := string(f.Severity)
		if _, seen := counts[sev]; !seen {
			order = append(order, sev)
		}
		counts[sev]++
	}
	parts := make([]string, 0, len(order))
	for _, sev := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[sev], sev))
	}
	countStr := strings.Join(parts, ", ")
	if countStr == "" {
		countStr = "no findings"
	}

	findingEntries := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		findingEntries = append(findingEntries, findingPayload(f))
	}

	payload := map[string]any{
		"title":                   title,
		"intent":                  CleanIntent(prIntent, MaxIntentChars),
		"reviewSummary":           fmt.Sprintf("PR-AF found %d finding(s) (%s).", len(findings), countStr),
		"findings":                findingEntries,
		"postLabel":               "Post selected",
		"rerunLabel":              "Re-review with instructions",
		"rejectLabel":             "Reject",
		"instructionsPlaceholder": "e.g. too aggressive, tone it down and drop the nitpicks",
	}
	if len(prMeta) > 0 {
		// Drop empties so optional zod fields stay absent rather than "".
		cleaned := map[string]any{}
		for k, v := range prMeta {
			if v != nil && v != "" {
				cleaned[k] = v
			}
		}
		if len(cleaned) > 0 {
			payload["pr"] = cleaned
		}
	}
	if revisionIter > 0 || len(revisionHistory) > 0 {
		prior := []string{}
		for _, ins := range revisionHistory {
			if strings.TrimSpace(ins) != "" {
				prior = append(prior, ins)
			}
		}
		payload["revision"] = map[string]any{
			"iteration":         revisionIter,
			"priorInstructions": prior,
		}
	}
	return payload, nil
}

// coerceStr ports review_gate.py::_coerce_str: a string is trimmed; a non-empty
// list yields its trimmed first string element (or a stringified first element);
// anything else yields "".
func coerceStr(value any) string {
	switch t := value.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		if len(t) > 0 {
			if s, ok := t[0].(string); ok {
				return strings.TrimSpace(s)
			}
			return fmt.Sprintf("%v", t[0])
		}
	}
	return ""
}

// coerceIDList ports review_gate.py::_coerce_id_list: a list yields its string
// elements; a non-empty string yields a single-element list; else empty.
func coerceIDList(value any) []string {
	switch t := value.(type) {
	case []any:
		out := []string{}
		for _, v := range t {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return []string{}
}

// idSet builds a set from a list of finding ids.
func idSet(ids []string) map[string]struct{} {
	s := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

// ParseReviewDecision converts an agentfield ApprovalResult into a
// ReviewDecision. Ports review_gate.py::parse_review_decision.
//
// Terminal control-plane outcomes (expired/error, or a hax-level reject with no
// form values) map to a reject. Otherwise the reviewer's action radio drives the
// outcome; an absent findings_to_post field defaults to posting everything (the
// form pre-checks all findings). res may be nil (defensive; Python's getattr
// tolerates a missing attribute), in which case it is treated as an empty
// approval.
func ParseReviewDecision(res *agent.ApprovalResult, allFindingIDs []string) ReviewDecision {
	var decision, feedback string
	var raw map[string]any
	if res != nil {
		decision = strings.TrimSpace(res.Decision)
		feedback = strings.TrimSpace(res.Feedback)
		raw = res.RawResponse
	}
	values := extractValuesFromRaw(raw)

	// Literal strings matching Python's hardcoded {"expired","error"} /
	// "rejected" set (equal to agent.ApprovalExpired/Error/Rejected).
	if decision == "expired" || decision == "error" {
		return ReviewDecision{Action: ActionReject, Instructions: feedback, DecisionRaw: decision}
	}
	if decision == "rejected" && len(values) == 0 {
		return ReviewDecision{Action: ActionReject, Instructions: feedback, DecisionRaw: decision}
	}

	action := coerceStr(values["action"])
	if _, ok := validActions[action]; !ok {
		if decision == "rejected" {
			action = ActionReject
		} else {
			action = ActionPost
		}
	}
	instructions := coerceStr(values["instructions"])
	if instructions == "" {
		instructions = feedback
	}

	if action == ActionRerun {
		return ReviewDecision{Action: ActionRerun, Instructions: instructions, DecisionRaw: decision}
	}
	if action == ActionReject {
		return ReviewDecision{Action: ActionReject, Instructions: instructions, DecisionRaw: decision}
	}

	// post_selected: honor the checked subset; default to all when field absent.
	var selected map[string]struct{}
	if _, present := values["findings_to_post"]; present {
		selected = idSet(coerceIDList(values["findings_to_post"]))
	} else {
		selected = idSet(allFindingIDs)
	}
	return ReviewDecision{
		Action:             ActionPost,
		SelectedFindingIDs: selected,
		Instructions:       instructions,
		DecisionRaw:        decision,
	}
}

// RequestReviewApprovalArgs are the inputs to RequestReviewApproval. App and
// Pauser are the two agent seams (Note and Pause); in production both are the
// same *agent.Agent, but they are separate fields so tests can drive a fake
// Pauser with a silent (nil) note surface.
type RequestReviewApprovalArgs struct {
	App             App
	Pauser          Pauser
	HaxClient       *HaxClient
	PRIntent        string
	Findings        []schemas.ScoredFinding
	PRLabel         string
	WebhookURL      *string
	UserID          *string
	ExpiresInHours  int
	PRMeta          map[string]any
	RevisionIter    int
	RevisionHistory []string
	Metadata        map[string]any
}

// buildPayloadFn is the payload builder RequestReviewApproval calls, indirected
// through a package var so tests can force the "payload build failed" reject
// path (BuildReviewPayload itself never errors in normal operation).
var buildPayloadFn = BuildReviewPayload

// RequestReviewApproval builds the payload, creates the hax request, pauses, and
// returns the decision. Ports review_gate.py::request_review_approval.
//
// It NEVER returns an error: any failure to build the payload, create the
// request, or pause is surfaced as a reject decision (with the exact
// "<stage> failed: <e>" instruction string) so the pipeline never posts an
// unreviewed review when the gate is enabled. The 120s create-request timeout
// comes from the hax substrate (HaxClient.CreateRequest).
func RequestReviewApproval(ctx context.Context, args RequestReviewApprovalArgs) ReviewDecision {
	title := "PR-AF Review Approval"
	if args.RevisionIter > 0 {
		title = fmt.Sprintf("%s (revision %d)", title, args.RevisionIter)
	}
	if args.PRLabel != "" {
		title = fmt.Sprintf("%s — %s", title, args.PRLabel)
	}

	payload, err := buildPayloadFn(args.PRIntent, args.Findings, title, args.PRMeta, args.RevisionIter, args.RevisionHistory)
	if err != nil {
		noteSafe(ctx, args.App, fmt.Sprintf("hitl: failed to build review payload: %s", err), "hitl", "payload", "error")
		return ReviewDecision{Action: ActionReject, Instructions: fmt.Sprintf("payload build failed: %s", err)}
	}

	noteSafe(ctx, args.App,
		fmt.Sprintf("hitl: submitting hax request (%s: %q)", HaxReviewTemplate, title),
		"hitl", "hax", "create_request")

	var webhookURL, userID string
	if args.WebhookURL != nil {
		webhookURL = *args.WebhookURL
	}
	if args.UserID != nil {
		userID = *args.UserID
	}

	created, err := args.HaxClient.CreateRequest(ctx, CreateRequestParams{
		Type:             HaxReviewTemplate,
		Payload:          payload,
		Title:            title,
		Description:      nil,
		WebhookURL:       webhookURL,
		ExpiresInSeconds: args.ExpiresInHours * 3600,
		UserID:           userID,
		Metadata:         args.Metadata,
	})
	if err != nil {
		noteSafe(ctx, args.App, fmt.Sprintf("hitl: create_request failed, treating as reject: %s", err), "hitl", "hax", "error")
		return ReviewDecision{Action: ActionReject, Instructions: fmt.Sprintf("create_request failed: %s", err)}
	}
	noteSafe(ctx, args.App,
		fmt.Sprintf("hitl: hax form request created (request_id=%s)", created.ID),
		"hitl", "hax", "submitted")

	approval, err := args.Pauser.Pause(ctx, agent.PauseOptions{
		ApprovalRequestID:  created.ID,
		ApprovalRequestURL: created.URL,
		ExpiresInHours:     args.ExpiresInHours,
	})
	if err != nil {
		noteSafe(ctx, args.App, fmt.Sprintf("hitl: pause failed, treating as reject: %s", err), "hitl", "pause", "error")
		return ReviewDecision{Action: ActionReject, Instructions: fmt.Sprintf("pause failed: %s", err)}
	}

	findingIDs := make([]string, 0, len(args.Findings))
	for _, f := range args.Findings {
		findingIDs = append(findingIDs, f.ID)
	}
	decision := ParseReviewDecision(approval, findingIDs)
	noteSafe(ctx, args.App,
		fmt.Sprintf("hitl: review decision=%s (raw=%s, selected=%d)",
			decision.Action, decision.DecisionRaw, len(decision.SelectedFindingIDs)),
		"hitl", "decision", decision.Action)
	return decision
}
