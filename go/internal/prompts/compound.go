package prompts

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Ports compound_finder_phase and compound_dedup_phase from
// reasoners/harnesses.py.

const compoundFinderPreamble = "You are a compound-risk investigator for PR findings. You are given a SMALL cluster " +
	"of findings that might interact. Your task is to investigate whether these findings " +
	"combine into something worse than each finding alone, then synthesize NEW first-class " +
	"findings when that combined risk is real.\n\n" +
	"Use repository access to verify interactions. Treat this as hypothesis-driven analysis, " +
	"not pattern matching: investigate whether there is a real chain or shared mechanism that " +
	"creates an issue an individual reviewer would likely miss.\n\n" +
	"Guidance for investigation depth:\n" +
	"- Check whether one finding creates a precondition that enables another.\n" +
	"- Check whether separately minor issues create an escalation path together.\n" +
	"- Check whether a safety mechanism exists in one place but is disconnected elsewhere.\n" +
	"- Check whether fixing one issue can worsen behavior exposed by another.\n" +
	"- Check whether repeated patterns indicate a systemic control gap.\n\n" +
	"Output contract:\n" +
	"- If no credible compound issue exists, return an empty findings list.\n" +
	"- If a compound issue exists, emit NEW findings only. Do not repeat original findings.\n" +
	"- Each output finding must include: title, severity, file_path, line_start, line_end, " +
	"body, evidence, suggestion, confidence, tags, and contributing_findings.\n" +
	"- `severity` MUST be exactly one of: `critical`, `important`, `suggestion`, `nitpick`.\n" +
	"- `contributing_findings` must list the exact titles from this cluster that combine.\n" +
	"- Only emit findings with confidence >= 0.6 and concrete evidence.\n\n"

// CompoundFinderPrompt ports compound_finder_phase. Caller invokes only when
// len(findings) >= 2. evidenceMap is keyed by finding title; each value is the
// raw evidence package (rendered verbatim into cluster_evidence and mined for
// the per-finding evidence_package sub-object).
func CompoundFinderPrompt(findings []schemas.ReviewFinding, repoPath string, evidenceMap map[string]*OMap) string {
	withContext := make([]*OMap, 0, 4)
	for _, f := range firstN(findings, 4) {
		entry := omap(
			"title", f.Title,
			"severity", string(f.Severity),
			"file_path", f.FilePath,
			"line_start", f.LineStart,
			"line_end", f.LineEnd,
			"dimension_name", f.DimensionName,
			"body", f.Body,
			"evidence", f.Evidence,
			"suggestion", f.Suggestion,
			"tags", orEmpty(f.Tags),
		)
		if ev, ok := evidenceMap[f.Title]; ok && ev != nil {
			entry.Set("evidence_package", omap(
				"primary_code", runeSlice(ev.GetStr("primary_code", ""), 4000),
				"import_context", runeSlice(ev.GetStr("import_context", ""), 2500),
				"caller_snippets", firstN(ev.GetStrSlice("caller_snippets"), 5),
				"related_code", runeSlice(ev.GetStr("related_code", ""), 2500),
				"cross_ref_snippets", firstN(ev.GetStrSlice("cross_ref_snippets"), 4),
			))
		}
		withContext = append(withContext, entry)
	}
	// cluster_evidence: unique finding titles (in list order) present in evidenceMap.
	clusterEvidence := omap()
	seen := map[string]bool{}
	for _, f := range findings {
		if seen[f.Title] {
			continue
		}
		seen[f.Title] = true
		if ev, ok := evidenceMap[f.Title]; ok {
			clusterEvidence.Set(f.Title, ev)
		}
	}
	summary := pyJSON(omap("cluster_findings", withContext, "cluster_evidence", clusterEvidence))

	var findingsRef string
	if utf8.RuneCountInString(summary) > 10000 && repoPath != "" {
		fp := contextPath(repoPath, ".pr-af-context", "compound_cluster_findings.json")
		findingsRef = "Cluster findings and evidence written to: " + fp +
			"\nRead this file for complete compound-analysis context."
	} else {
		findingsRef = "Cluster context:\n" + summary
	}
	return compoundFinderPreamble + findingsRef + "\n\nReturn strict JSON matching the schema."
}

const compoundDedupPreamble = "You are a deduplication specialist reviewing compound findings from a PR review.\n\n" +
	"Compound findings are synthesized from clusters of individual findings. Because " +
	"clusters are analyzed independently and in parallel, different clusters sometimes " +
	"produce findings that cover the SAME underlying insight from slightly different " +
	"angles.\n\n" +
	"Your task: identify which compound findings represent genuinely DISTINCT insights " +
	"and which are near-duplicates. Two findings are duplicates when they describe the " +
	"same root cause, same attack vector, or same systemic pattern — even if phrased " +
	"differently or using different terminology.\n\n" +
	"When duplicates exist, keep the finding that is:\n" +
	"- Most specific and actionable\n" +
	"- Best evidenced\n" +
	"- Highest severity\n\n" +
	"Also check: does any compound finding merely RESTATE what an individual finding " +
	"already says without adding a genuinely new cross-cutting insight? If so, drop it.\n\n"

// CompoundDedupPrompt ports compound_dedup_phase. Caller invokes only when
// len(findings) > 1. The Tags field interpolates as a Python list repr
// (single-quoted).
func CompoundDedupPrompt(findings []schemas.ReviewFinding, individualFindingsSummary string) string {
	numbered := make([]string, len(findings))
	for idx, f := range findings {
		numbered[idx] = "[" + strconv.Itoa(idx) + "] Title: " + f.Title + "\n" +
			"    Severity: " + string(f.Severity) + "\n" +
			"    File: " + f.FilePath + "\n" +
			"    Tags: " + pyRepr(orEmpty(f.Tags)) + "\n" +
			"    Body: " + runeSlice(f.Body, 500) + "\n" +
			"    Evidence: " + runeSlice(f.Evidence, 300)
	}
	findingsText := strings.Join(numbered, "\n\n")

	individualContext := ""
	if individualFindingsSummary != "" {
		individualContext = "\n\nFor reference, these are the INDIVIDUAL findings that the compound " +
			"findings were synthesized from:\n" + individualFindingsSummary
	}

	return compoundDedupPreamble +
		"COMPOUND FINDINGS TO EVALUATE (" + strconv.Itoa(len(findings)) + " total):\n\n" +
		findingsText + individualContext +
		"\n\nReturn `keep_indices` as a list of 0-based indices of findings to KEEP. " +
		"Include your reasoning."
}
