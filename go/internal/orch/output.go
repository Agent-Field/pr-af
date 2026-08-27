package orch

// output.go ports Phase 8 (_generate_output) and every Markdown builder from
// orchestrator.py. The summary/comment Markdown is reproduced BYTE-FOR-BYTE
// (footer badges/links, verdict wording, severity emojis) — see the golden test.
//
// Float parity (design risk 3): duration is round(elapsed, 1) rendered with
// Python str(float) semantics via pyFloatStr; confidence percentages use int()
// truncation, which matches IEEE-754 across Go and Python.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/gates"
	"github.com/Agent-Field/pr-af/go/internal/github"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
	"github.com/Agent-Field/pr-af/go/internal/scoring"
)

// severityRank ports the comment-eligibility rank map.
var severityRank = map[string]int{"nitpick": 0, "suggestion": 1, "important": 2, "critical": 3}

func (o *Orchestrator) generateOutput(
	ctx context.Context,
	scoredFindings []schemas.ScoredFinding,
	intake schemas.IntakeResult,
	anatomy schemas.AnatomyResult,
	plan schemas.ReviewPlan,
	postToGitHub bool,
) (schemas.ReviewResult, error) {
	if o.prData == nil {
		return schemas.ReviewResult{}, errPRDataNotInitialized
	}
	if err := o.validateReviewCompletion(plan); err != nil {
		return schemas.ReviewResult{}, err
	}

	diffFiles := map[string]struct{}{}
	for _, cf := range o.prData.ChangedFiles {
		diffFiles[cf.Path] = struct{}{}
	}
	diffRanges := o.diffLineRanges()

	minRank, ok := severityRank[o.config.Comments.MinSeverity]
	if !ok {
		minRank = 1
	}

	comments := []schemas.GitHubComment{}
	var filteredForComments []schemas.ScoredFinding
	for _, finding := range scoredFindings {
		if severityRank[string(finding.Severity)] < minRank {
			continue
		}
		filteredForComments = append(filteredForComments, finding)
		normPath := o.normalizePath(finding.FilePath)
		if normPath == "" || !contains(diffFiles, normPath) || finding.LineStart <= 0 {
			continue
		}
		inRange := false
		for _, r := range diffRanges[normPath] {
			if r[0] <= finding.LineStart && finding.LineStart <= r[1] {
				inRange = true
				break
			}
		}
		if !inRange {
			continue
		}
		comments = append(comments, schemas.GitHubComment{
			Path: normPath,
			Line: finding.LineStart,
			Side: finding.DiffSide,
			Body: o.formatCommentBody(finding),
		})
	}

	if len(comments) > o.config.Comments.MaxComments {
		comments = comments[:o.config.Comments.MaxComments]
	}
	if o.config.Comments.PolishEnabled && len(comments) > 0 {
		comments = gates.PolishComments(ctx, o.deps.App, comments)
	}

	// Decorate each comment with the blocking/advisory callout for its finding.
	type locKey struct {
		path string
		line int
		side string
	}
	findingsByLocation := map[locKey]schemas.ScoredFinding{}
	for _, f := range filteredForComments {
		findingsByLocation[locKey{o.normalizePath(f.FilePath), f.LineStart, f.DiffSide}] = f
	}
	decorated := make([]schemas.GitHubComment, len(comments))
	for i, c := range comments {
		var fptr *schemas.ScoredFinding
		if f, ok := findingsByLocation[locKey{c.Path, c.Line, c.Side}]; ok {
			ff := f
			fptr = &ff
		}
		nc := c
		nc.Body = decorateWithBlocking(fptr, c.Body)
		decorated[i] = nc
	}
	comments = decorated

	reviewEvent := scoring.DetermineReviewEvent(filteredForComments)
	summaryBody := o.formatSummary(filteredForComments, reviewEvent, &intake, &plan)

	review := schemas.GitHubReview{
		Body:     summaryBody,
		Event:    reviewEvent,
		Comments: comments,
	}

	if postToGitHub && !o.input.DryRun && strp(o.input.PrURL) != "" {
		o.postReview(ctx, review, summaryBody, comments)
	}

	bySeverity := map[string]int{}
	for _, f := range scoredFindings {
		bySeverity[string(f.Severity)]++
	}
	blockingCount, advisoryCount := 0, 0
	for _, f := range scoredFindings {
		if f.Blocking {
			blockingCount++
		} else {
			advisoryCount++
		}
	}

	summary := schemas.ReviewSummary{
		TotalFindings:         len(scoredFindings),
		BySeverity:            bySeverity,
		BlockingCount:         blockingCount,
		AdvisoryCount:         advisoryCount,
		DimensionsRun:         len(plan.Dimensions),
		CrossRefInteractions:  o.crossRefCount,
		AdversaryChallenged:   o.adversaryChallengedCount,
		AdversaryConfirmed:    o.adversaryConfirmedCount,
		CoverageIterations:    o.coverageIterations,
		AIGeneratedConfidence: intake.AIGenerated,
		CostUSD:               localPyRound(o.totalCost(), 4),
		DurationSeconds:       localPyRound(o.clock().Seconds(), 3),
		BudgetExhausted:       o.isBudgetExhausted(),
	}

	intakeMap, _ := structToMap(&intake)
	anatomyMap, _ := structToMap(&anatomy)
	planMap, _ := structToMap(&plan)
	budgetBreakdown := map[string]any{}
	o.mu.Lock()
	for k, v := range o.costBreakdown {
		budgetBreakdown[k] = v
	}
	total := o.totalCostUSD
	exhausted := o.budgetExhausted
	o.mu.Unlock()
	dimensionStats := o.dimensionStats()
	evidenceEligible, evidenceAttempted, adversaryEligible, adversaryAttempted := o.conditionalPhaseStats()
	requestMetadata := map[string]any{}
	if o.input.PublisherJobID != "" {
		requestMetadata["publisher_job_id"] = o.input.PublisherJobID
	}
	if o.input.PullRequest != nil {
		pullRequest, _ := structToMap(o.input.PullRequest)
		requestMetadata["pull_request"] = pullRequest
	}
	o.markPhaseCompleted("output")
	metadata := schemas.ReviewMetadata{
		Intake:  intakeMap,
		Anatomy: anatomyMap,
		Plan:    planMap,
		Budget: map[string]any{
			"total_cost_usd":       total,
			"cost_breakdown":       budgetBreakdown,
			"budget_exhausted":     exhausted,
			"max_cost_usd":         o.config.Budget.MaxCostUSD,
			"max_duration_seconds": o.config.Budget.MaxDurationSeconds,
		},
		Request:                    requestMetadata,
		AgentInvocations:           o.invocations(),
		PipelineContractVersion:    1,
		PhasesCompleted:            o.completedPhaseNames(),
		ReviewDimensionsAttempted:  dimensionStats.Attempted,
		ReviewDimensionsParseable:  dimensionStats.Parseable,
		DegradedDimensions:         dimensionStats.Failed,
		EvidenceFindingsEligible:   evidenceEligible,
		EvidenceFindingsAttempted:  evidenceAttempted,
		AdversaryFindingsEligible:  adversaryEligible,
		AdversaryFindingsAttempted: adversaryAttempted,
	}

	prURL := strp(o.input.PrURL)
	if prURL == "" && o.input.PullRequest != nil {
		prURL = o.input.PullRequest.URL
	}
	return schemas.ReviewResult{
		ReviewID: o.reviewID,
		PrURL:    prURL,
		Review:   review,
		Findings: scoredFindings,
		Summary:  summary,
		Metadata: metadata,
	}, nil
}

// validateReviewCompletion prevents scoring or publication metadata from
// representing an empty or unexecuted review plan as a clean result.
func (o *Orchestrator) validateReviewCompletion(plan schemas.ReviewPlan) error {
	for _, phase := range requiredPreOutputPhases {
		if !o.hasCompletedPhase(phase) {
			return fmt.Errorf("required review phase %q did not complete", phase)
		}
	}
	if len(plan.Dimensions) == 0 {
		return errors.New("review plan contains zero dimensions")
	}
	stats := o.dimensionStats()
	if stats.Attempted == 0 {
		return errors.New("no review dimensions were attempted")
	}
	if stats.Attempted < len(plan.Dimensions) {
		return fmt.Errorf("only %d of %d planned review dimensions were attempted", stats.Attempted, len(plan.Dimensions))
	}
	if stats.Parseable == 0 {
		return errors.New("no review dimensions produced schema-valid output")
	}
	if stats.Attempted != stats.Parseable+stats.Failed {
		return fmt.Errorf(
			"review dimension accounting is incomplete: attempted=%d parseable=%d failed=%d",
			stats.Attempted,
			stats.Parseable,
			stats.Failed,
		)
	}
	evidenceEligible, evidenceAttempted, adversaryEligible, adversaryAttempted := o.conditionalPhaseStats()
	if evidenceEligible != evidenceAttempted {
		return fmt.Errorf(
			"evidence verification accounting is incomplete: eligible=%d attempted=%d",
			evidenceEligible,
			evidenceAttempted,
		)
	}
	if evidenceEligible > 0 && !o.hasCompletedPhase("evidence_verification") {
		return errors.New("eligible findings did not complete evidence verification")
	}
	if adversaryEligible != adversaryAttempted {
		return fmt.Errorf(
			"adversary accounting is incomplete: eligible=%d attempted=%d",
			adversaryEligible,
			adversaryAttempted,
		)
	}
	if adversaryEligible > 0 && !o.hasCompletedPhase("adversary") {
		return errors.New("eligible findings did not complete adversary analysis")
	}
	return nil
}

// postReview posts the review, downgrading a 422 "own pull request" REQUEST_
// CHANGES to a COMMENT and retrying once (orchestrator.py's httpx handling).
func (o *Orchestrator) postReview(ctx context.Context, review schemas.GitHubReview, summaryBody string, comments []schemas.GitHubComment) {
	_, err := o.deps.GH.PostReview(ctx, o.prData.Owner, o.prData.Repo, o.prData.Number, review, o.prData.HeadSHA)
	if err == nil {
		return
	}
	var apiErr *github.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 422 && strings.Contains(strings.ToLower(apiErr.Body), "own pull request") {
		fallback := schemas.GitHubReview{Body: summaryBody, Event: "COMMENT", Comments: comments}
		if _, retryErr := o.deps.GH.PostReview(ctx, o.prData.Owner, o.prData.Repo, o.prData.Number, fallback, o.prData.HeadSHA); retryErr != nil {
			fmt.Printf("[PR-AF] Failed to post review on retry: %v\n", retryErr)
		}
		return
	}
	fmt.Printf("[PR-AF] Failed to post review: %v\n", err)
}

// ---- path / range helpers ----

func (o *Orchestrator) normalizePath(path string) string {
	if path == "" {
		return path
	}
	repoPath := strp(o.input.RepoPath)
	if repoPath != "" && strings.HasPrefix(path, repoPath) {
		path = strings.TrimLeft(path[len(repoPath):], "/")
	}
	if strings.HasPrefix(path, "/workspaces/") {
		parts := strings.SplitN(path, "/", 4)
		if len(parts) >= 4 {
			path = parts[3]
		}
	}
	return path
}

var hunkRe = regexp.MustCompile(`\+(\d+)(?:,(\d+))?`)

func (o *Orchestrator) diffLineRanges() map[string][][2]int {
	ranges := map[string][][2]int{}
	if o.prData == nil {
		return ranges
	}
	for _, cf := range o.prData.ChangedFiles {
		if cf.Patch == "" {
			ranges[cf.Path] = [][2]int{{1, 999999}}
			continue
		}
		var fileRanges [][2]int
		for _, line := range strings.Split(cf.Patch, "\n") {
			if !strings.HasPrefix(line, "@@") {
				continue
			}
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			fileRanges = append(fileRanges, [2]int{start, start + count})
		}
		if len(fileRanges) > 0 {
			ranges[cf.Path] = fileRanges
		} else {
			ranges[cf.Path] = [][2]int{{1, 999999}}
		}
	}
	return ranges
}

// ---- comment body ----

func (o *Orchestrator) formatCommentBody(finding schemas.ScoredFinding) string {
	emoji := o.config.Comments.SeverityEmojis[string(finding.Severity)]
	severityLabel := strings.ToUpper(string(finding.Severity))
	lines := []string{
		"**" + finding.Title + "**",
		"",
		fmt.Sprintf("<sub>%s `%s` · confidence %d%%</sub>", emoji, severityLabel, int(finding.Confidence*100)),
		"",
		strings.TrimSpace(finding.Body),
	}

	if finding.Evidence != "" {
		lines = append(lines, "", "<details><summary>Evidence</summary>", "")
		for _, evLine := range splitLines(strings.TrimSpace(finding.Evidence)) {
			lines = append(lines, "> "+evLine)
		}
		lines = append(lines, "", "</details>")
	}

	if o.config.Comments.IncludeSuggestions && finding.Suggestion != nil && *finding.Suggestion != "" {
		suggestionText := strings.TrimSpace(*finding.Suggestion)
		if o.config.Comments.SuggestionMode == "code" {
			lines = append(lines, "", "```suggestion", suggestionText, "```")
		} else {
			lines = append(lines, "", "**💡 Suggested Fix**", "", suggestionText)
		}
	}

	var metaParts []string
	if o.config.Comments.IncludeDimensionAttribution {
		metaParts = append(metaParts, "`"+finding.DimensionName+"`")
	}
	if o.config.Comments.IncludeConfidence {
		metaParts = append(metaParts, fmt.Sprintf("confidence %d%%", int(finding.Confidence*100)))
	}
	if len(metaParts) > 0 {
		lines = append(lines, "", "---", "*"+strings.Join(metaParts, " · ")+"*")
	}

	lines = append(lines, "", "<sub>🤖 Reviewed by [AgentField PR-AF](https://github.com/Agent-Field/pr-af)</sub>")
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func decorateWithBlocking(finding *schemas.ScoredFinding, body string) string {
	var header string
	if finding != nil && finding.Blocking {
		header = "> [!CAUTION]\n> **Must-fix before merge.**"
		if finding.BlockingReason != "" {
			header += " " + finding.BlockingReason
		}
	} else {
		header = "> [!NOTE]\n> **Advisory — non-blocking.** Safe to merge and address in a follow-up."
		if finding != nil && finding.BlockingReason != "" {
			header += "\n>\n> _Why non-blocking:_ " + finding.BlockingReason
		}
	}
	return header + "\n\n" + body
}

// ---- summary Markdown ----

func (o *Orchestrator) formatSummary(
	findings []schemas.ScoredFinding,
	reviewEvent string,
	intake *schemas.IntakeResult,
	plan *schemas.ReviewPlan,
) string {
	_ = reviewEvent
	bySeverity := map[string]int{"critical": 0, "important": 0, "suggestion": 0, "nitpick": 0}
	for _, f := range findings {
		bySeverity[string(f.Severity)]++
	}
	emojis := o.config.Comments.SeverityEmojis
	duration := localPyRound(o.clock().Seconds(), 1)

	blockingCount := 0
	for _, f := range findings {
		if f.Blocking {
			blockingCount++
		}
	}
	advisoryCount := len(findings) - blockingCount
	emoji, label := computeRatingV2(blockingCount, advisoryCount, len(findings))

	var verdictLine string
	switch {
	case blockingCount == 0 && advisoryCount == 0:
		verdictLine = "> ✅ **Safe to merge.** No findings from automated review."
	case blockingCount == 0:
		verdictLine = fmt.Sprintf("> ✅ **Safe to merge.** %d advisory note%s below — non-blocking, safe to address in a follow-up.",
			advisoryCount, plural(advisoryCount))
	default:
		verdictLine = fmt.Sprintf("> 🚫 **Merge blocked.** %d must-fix issue%s found by automated review.",
			blockingCount, plural(blockingCount))
	}

	var lines []string
	lines = append(lines,
		fmt.Sprintf("## %s PR-AF Review — **%s**", emoji, label),
		"",
		verdictLine,
		"",
		"*Automated multi-agent code review · [PR-AF](https://github.com/Agent-Field/pr-af) built with [AgentField](https://github.com/Agent-Field/agentfield)*",
		"",
		fmt.Sprintf("> **%d findings** · 🚫 %d blocking · 💬 %d advisory · %s %d critical · %s %d important · %s %d suggestions · %s %d nitpicks",
			len(findings), blockingCount, advisoryCount,
			emojis["critical"], bySeverity["critical"],
			emojis["important"], bySeverity["important"],
			emojis["suggestion"], bySeverity["suggestion"],
			emojis["nitpick"], bySeverity["nitpick"]),
		"",
	)
	if degraded := o.dimensionStats().Failed; degraded > 0 {
		lines = append(lines, fmt.Sprintf("> Degraded dimensions: %d", degraded), "")
	}

	if intake != nil {
		lines = append(lines, "<details>", "<summary><b>PR Overview</b></summary>", "", intake.PrSummary, "", "</details>", "")
	}

	lines = append(lines, o.buildKeyFindings(findings)...)

	if len(findings) > 0 {
		lines = append(lines, "<details>", "<summary><b>All Findings by Severity</b></summary>", "")
		for _, sev := range []string{"critical", "important", "suggestion", "nitpick"} {
			var sevFindings []schemas.ScoredFinding
			for _, f := range findings {
				if string(f.Severity) == sev {
					sevFindings = append(sevFindings, f)
				}
			}
			if len(sevFindings) == 0 {
				continue
			}
			lines = append(lines, fmt.Sprintf("#### %s %s (%d)", emojis[sev], titleCase(sev), len(sevFindings)))
			lines = append(lines, "")
			for _, f := range sevFindings {
				pathRef := ""
				if f.FilePath != "" {
					pathRef = fmt.Sprintf("`%s:%d`", o.normalizePath(f.FilePath), f.LineStart)
				}
				lines = append(lines, fmt.Sprintf("- **%s** %s", f.Title, pathRef))
			}
			lines = append(lines, "")
		}
		lines = append(lines, "</details>", "")
	}

	lines = append(lines, o.buildReviewDetails(findings, plan)...)
	lines = append(lines, o.buildPipelineStats(intake, duration)...)
	lines = append(lines, "Review ID: `"+o.reviewID+"`")
	lines = append(lines,
		"",
		"<br>",
		`<div align="right">`,
		`  <a href="https://github.com/Agent-Field/pr-af">`,
		`    <img src="https://img.shields.io/badge/Powered_by-AgentField-6366f1?style=flat-square&logo=github" alt="AgentField PR-AF"/>`,
		"  </a>",
		"</div>",
	)

	return strings.Join(lines, "\n")
}

func computeRatingV2(blocking, advisory, total int) (emoji, label string) {
	switch {
	case total == 0:
		return "🟢", "Looks Good"
	case blocking >= 3:
		return "🔴", "Multiple Merge-Blockers"
	case blocking >= 1:
		return "🔴", "Merge Blocked — Must-Fix Found"
	case advisory >= 10:
		return "🟢", "Safe to Merge — Many Advisories"
	case advisory >= 3:
		return "🟢", "Safe to Merge — Advisories Only"
	default:
		return "🟢", "Safe to Merge — Minor Advisories"
	}
}

func (o *Orchestrator) buildKeyFindings(findings []schemas.ScoredFinding) []string {
	if len(findings) == 0 {
		return []string{"**No issues found.** This PR looks clean across all review dimensions.", ""}
	}
	var lines []string
	var blocking, nonBlocking []schemas.ScoredFinding
	for _, f := range findings {
		if f.Blocking {
			blocking = append(blocking, f)
		} else {
			nonBlocking = append(nonBlocking, f)
		}
	}

	lines = append(lines, "### Key Findings", "")

	if len(blocking) > 0 {
		lines = append(lines, fmt.Sprintf("**%d issue(s) should be addressed before merge:**", len(blocking)), "")
		for _, f := range firstNScored(blocking, 8) {
			emoji := o.config.Comments.SeverityEmojis[string(f.Severity)]
			pathRef := ""
			if f.FilePath != "" {
				pathRef = fmt.Sprintf(" (`%s:%d`)", o.normalizePath(f.FilePath), f.LineStart)
			}
			reason := ""
			if f.BlockingReason != "" {
				reason = " Gate: " + f.BlockingReason
			}
			lines = append(lines, fmt.Sprintf("- %s **%s**%s — %s%s", emoji, f.Title, pathRef, firstSentence(f.Body), reason))
		}
		if len(blocking) > 8 {
			lines = append(lines, fmt.Sprintf("- … and %d more (see All Findings by Severity)", len(blocking)-8))
		}
		lines = append(lines, "")
	}

	if len(nonBlocking) > 0 {
		lines = append(lines, fmt.Sprintf("**%d advisory finding(s) surfaced as non-blocking:**", len(nonBlocking)), "")
		for _, f := range firstNScored(nonBlocking, 5) {
			emoji := o.config.Comments.SeverityEmojis[string(f.Severity)]
			pathRef := ""
			if f.FilePath != "" {
				pathRef = fmt.Sprintf(" (`%s:%d`)", o.normalizePath(f.FilePath), f.LineStart)
			}
			lines = append(lines, fmt.Sprintf("- %s %s%s", emoji, f.Title, pathRef))
		}
		if len(nonBlocking) > 5 {
			lines = append(lines, fmt.Sprintf("- … and %d more (see All Findings by Severity)", len(nonBlocking)-5))
		}
		lines = append(lines, "")
	}

	affectedSet := map[string]struct{}{}
	for _, f := range findings {
		if f.FilePath != "" {
			affectedSet[o.normalizePath(f.FilePath)] = struct{}{}
		}
	}
	affected := make([]string, 0, len(affectedSet))
	for p := range affectedSet {
		affected = append(affected, p)
	}
	sort.Strings(affected)
	if len(affected) > 0 {
		shown := affected
		if len(shown) > 10 {
			shown = shown[:10]
		}
		quoted := make([]string, len(shown))
		for i, p := range shown {
			quoted[i] = "`" + p + "`"
		}
		lines = append(lines, "**Files with findings:** "+strings.Join(quoted, ", "))
		if len(affected) > 10 {
			lines = append(lines, fmt.Sprintf(" … and %d more", len(affected)-10))
		}
		lines = append(lines, "")
	}

	return lines
}

func (o *Orchestrator) buildReviewDetails(findings []schemas.ScoredFinding, plan *schemas.ReviewPlan) []string {
	var detailParts []string

	if plan != nil && len(plan.Dimensions) > 0 {
		detailParts = append(detailParts, fmt.Sprintf("**Dimensions Analyzed (%d):**", len(plan.Dimensions)), "")
		for _, dim := range plan.Dimensions {
			detailParts = append(detailParts, fmt.Sprintf("- **%s** — %d file(s)", dim.Name, len(dim.TargetFiles)))
		}
		detailParts = append(detailParts, "")
	}

	if len(o.metaSelectorResults) > 0 {
		detailParts = append(detailParts, fmt.Sprintf("**Meta-Dimension Lenses (%d):**", len(o.metaSelectorResults)), "")
		for _, meta := range o.metaSelectorResults {
			detailParts = append(detailParts, fmt.Sprintf("- **%s** — %d dimension(s), %d%% coverage confidence",
				titleCase(meta.Lens), len(meta.Dimensions), int(meta.Confidence*100)))
		}
		detailParts = append(detailParts, "")
	}

	subReviewSet := map[string]struct{}{}
	for _, f := range findings {
		if strings.Contains(f.DimensionName, "→") {
			subReviewSet[f.DimensionName] = struct{}{}
		}
	}
	if len(subReviewSet) > 0 {
		detailParts = append(detailParts, fmt.Sprintf("**Sub-Reviews Spawned (%d deep-dives):**", len(subReviewSet)), "")
		names := make([]string, 0, len(subReviewSet))
		for n := range subReviewSet {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, dimName := range names {
			count := 0
			for _, f := range findings {
				if f.DimensionName == dimName {
					count++
				}
			}
			detailParts = append(detailParts, fmt.Sprintf("- **%s** (%d finding(s))", dimName, count))
		}
		detailParts = append(detailParts, "")
	}

	if o.crossRefCount > 0 || o.adversaryConfirmedCount > 0 || o.adversaryChallengedCount > 0 {
		detailParts = append(detailParts, "**Cross-Reference & Adversary Analysis:**", "")
		if o.crossRefCount > 0 {
			detailParts = append(detailParts, fmt.Sprintf("- **%d** compound finding(s) synthesized", o.crossRefCount))
		}
		totalAdv := o.adversaryConfirmedCount + o.adversaryChallengedCount
		if totalAdv > 0 {
			detailParts = append(detailParts, fmt.Sprintf("- **%d** finding(s) adversarially tested: %d confirmed, %d challenged",
				totalAdv, o.adversaryConfirmedCount, o.adversaryChallengedCount))
		}
		detailParts = append(detailParts, "")
	}

	if len(detailParts) == 0 {
		return nil
	}
	out := []string{"<details>", "<summary><b>Review Process Details</b></summary>", ""}
	out = append(out, detailParts...)
	out = append(out, "</details>", "")
	return out
}

func (o *Orchestrator) buildPipelineStats(intake *schemas.IntakeResult, duration float64) []string {
	total := o.totalCost()
	costDisplay := "N/A (provider does not report cost)"
	if total > 0 {
		costDisplay = fmt.Sprintf("$%.4f", total)
	}
	exhaustionReason := ""
	if o.isBudgetExhausted() {
		elapsed := o.clock().Seconds()
		if elapsed > float64(o.config.Budget.MaxDurationSeconds) {
			exhaustionReason = fmt.Sprintf(" (timeout: %ds > %ds limit)", int(elapsed), o.config.Budget.MaxDurationSeconds)
		} else if total >= o.config.Budget.MaxCostUSD {
			exhaustionReason = fmt.Sprintf(" (cost: $%.2f ≥ $%.2f limit)", total, o.config.Budget.MaxCostUSD)
		}
	}
	budgetExhaustedCell := "No"
	if o.isBudgetExhausted() {
		budgetExhaustedCell = "Yes" + exhaustionReason
	}

	statsRows := []string{
		fmt.Sprintf("| Duration | %ss |", pyFloatStr(duration)),
		fmt.Sprintf("| Agent invocations | %d |", o.invocations()),
		fmt.Sprintf("| Coverage iterations | %d |", o.coverageIterations),
		fmt.Sprintf("| Estimated cost | %s |", costDisplay),
		fmt.Sprintf("| Budget exhausted | %s |", budgetExhaustedCell),
	}
	if intake != nil {
		statsRows = append(statsRows,
			fmt.Sprintf("| PR type | %s |", intake.PrType),
			fmt.Sprintf("| Complexity | %s |", intake.Complexity))
	}

	out := []string{"<details>", "<summary><b>Pipeline Stats</b></summary>", "", "| Metric | Value |", "|--------|-------|"}
	out = append(out, statsRows...)
	out = append(out, "", "</details>", "")
	return out
}

// firstSentence ports _first_sentence.
func firstSentence(text string) string {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
	for _, sep := range []string{". ", ".\n", "! ", "?\n"} {
		idx := strings.Index(text, sep)
		if idx != -1 && idx < 200 {
			return runeSlice(text, 0, idx+1)
		}
	}
	if runeLen(text) > 200 {
		return runeSlice(text, 0, 200) + "…"
	}
	return text
}

// ---- small text helpers ----

func plural(n int) string {
	if n != 1 {
		return "s"
	}
	return ""
}

func contains(m map[string]struct{}, key string) bool {
	_, ok := m[key]
	return ok
}

// titleCase ports Python str.title() for the single lowercase words used here
// (severity/lens names): capitalize the first letter of each run of letters.
func titleCase(s string) string {
	var b strings.Builder
	prevAlpha := false
	for _, r := range s {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		switch {
		case isAlpha && !prevAlpha:
			b.WriteRune(toUpperRune(r))
		case isAlpha:
			b.WriteRune(toLowerRune(r))
		default:
			b.WriteRune(r)
		}
		prevAlpha = isAlpha
	}
	return b.String()
}

func toUpperRune(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 32
	}
	return r
}

func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

// splitLines ports Python str.splitlines() (no trailing empty element for a
// trailing newline; splits on \n, \r, \r\n).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	normalized := strings.ReplaceAll(s, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	parts := strings.Split(normalized, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func firstNScored(xs []schemas.ScoredFinding, n int) []schemas.ScoredFinding {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}

func runeLen(s string) int { return len([]rune(s)) }

func runeSlice(s string, start, end int) string {
	r := []rune(s)
	if end > len(r) {
		end = len(r)
	}
	return string(r[start:end])
}

// localPyRound reproduces Python round(x, digits) (round-half-to-even) via
// correctly-rounded decimal formatting, matching scoring.pyRound (private there).
func localPyRound(x float64, digits int) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', digits, 64), 64)
	return v
}

// pyFloatStr renders a float the way Python str(float) does: shortest round-trip
// decimal, always with a decimal point (or exponent). Used for the `{duration}s`
// cell so a whole-second duration renders "12.0", not "12".
func pyFloatStr(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	return s
}
