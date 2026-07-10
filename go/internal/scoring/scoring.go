// Package scoring is the deterministic scoring engine for PR-AF — a byte-exact
// port of src/pr_af/scoring.py.
//
// LLMs reason about issues; this code computes scores. The same findings always
// produce the same scores, so scoring is auditable, testable, and tunable
// independently of the agents. Following the Contract-AF / SEC-AF pattern,
// scoring is intentionally separated from agent code.
//
// Parity notes vs. Python (see design §B.3, §D):
//   - This package keeps its OWN, deliberately DIVERGENT severity alias map
//     (sevAlias): medium->important, low->suggestion, and it recognizes
//     "trivia". It disagrees with the canonical schemas.NormalizeSeverity map
//     (medium->suggestion, low->nitpick) and MUST NOT be shared with it.
//   - pyRound reproduces Python's round() semantics (round-half-to-even on the
//     true binary value) wherever scoring.py calls round(x, 3). Go's math.Round
//     is half-away-from-zero and would diverge (e.g. 0.0625 -> 0.063 vs 0.062).
//   - ScoreFindings assigns positional ids f_%03d in pre-sort iteration order,
//     then stably sorts by score descending — matching Python's stable
//     list.sort(key=..., reverse=True), which preserves the input order of
//     equal-scored findings.
package scoring

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// pyRound reproduces Python's built-in round(x, digits): round-half-to-even
// ("banker's rounding") applied to the true decimal value of the float64.
//
// strconv.FormatFloat with the 'f' verb and a fixed precision performs
// correctly-rounded, round-to-nearest-ties-to-even decimal conversion — exactly
// what CPython's round() does — so formatting then re-parsing yields the same
// float Python would produce. This is deliberately NOT math.Round(x*pow)/pow,
// which rounds half away from zero and additionally accumulates the x*pow error
// (e.g. 0.0625 -> 0.063 instead of Python's 0.062).
func pyRound(x float64, digits int) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', digits, 64), 64)
	return v
}

// sevAlias is the scoring engine's PRIVATE, DIVERGENT severity normalization map
// (scoring.py:45-62). Reviewer LLMs sometimes emit uppercase or aliases like
// "high"/"medium"; this maps them onto the canonical rubric so downstream code
// (base-weight lookup, confidence-threshold gates) stays consistent.
//
// It intentionally DIFFERS from schemas.severityAliases: here medium->important
// and low->suggestion (one rung higher), and it recognizes "trivia". Do not
// replace this with schemas.NormalizeSeverity.
var sevAlias = map[string]string{
	"critical":   "critical",
	"high":       "critical",
	"blocker":    "critical",
	"important":  "important",
	"medium":     "important",
	"major":      "important",
	"suggestion": "suggestion",
	"minor":      "suggestion",
	"low":        "suggestion",
	"nitpick":    "nitpick",
	"info":       "nitpick",
	"trivia":     "nitpick",
	"trivial":    "nitpick",
}

// normSev ports scoring.py _norm_sev: case-insensitive, whitespace-tolerant
// lookup in sevAlias; empty or unknown input falls back to "suggestion".
func normSev(s string) string {
	if v, ok := sevAlias[strings.ToLower(strings.TrimSpace(s))]; ok {
		return v
	}
	return "suggestion"
}

// ScoreFindings scores, ranks, and filters findings (scoring.py score_findings).
//
// Steps:
//  1. Apply base severity weights.
//  2. Apply multipliers from adversary verdicts and global context.
//  3. Filter out findings below their severity's confidence threshold.
//  4. Synthesize adversary "missed_trap" findings.
//  5. Stably sort by composite score descending.
//
// Positional ids (f_%03d) are assigned in pre-sort iteration order.
func ScoreFindings(
	findings []schemas.ReviewFinding,
	adversaryResults []schemas.AdversaryResult,
	cfg config.ScoringConfig,
	aiGenerated float64,
	blastRadiusSize int,
) []schemas.ScoredFinding {
	// Index adversary results by finding title (last write wins on collisions,
	// matching Python's dict comprehension).
	adversaryByTitle := make(map[string]schemas.AdversaryResult, len(adversaryResults))
	for _, ar := range adversaryResults {
		adversaryByTitle[ar.FindingTitle] = ar
	}

	scored := []schemas.ScoredFinding{}

	for _, finding := range findings {
		ns := normSev(string(finding.Severity))

		// Base weight from severity (default 0.3 when absent).
		base, ok := cfg.BaseWeights[ns]
		if !ok {
			base = 0.3
		}

		// Confidence-weighted base.
		score := base * finding.Confidence

		// Collect active multipliers.
		activeMultipliers := []string{}

		// Adversary assessment.
		if adversary, ok := adversaryByTitle[finding.Title]; ok {
			switch adversary.Verdict {
			case "confirmed":
				score *= multiplier(cfg, "adversary_confirmed", 1.3)
				activeMultipliers = append(activeMultipliers, "adversary_confirmed")
			case "challenged":
				score *= multiplier(cfg, "adversary_challenged", 0.5)
				activeMultipliers = append(activeMultipliers, "adversary_challenged")
			}
		}

		// AI-generated PR multiplier.
		if aiGenerated > 0.5 {
			score *= multiplier(cfg, "ai_generated_pr", 1.2)
			activeMultipliers = append(activeMultipliers, "ai_generated_pr")
		}

		// Blast radius multiplier.
		if blastRadiusSize > 10 {
			score *= multiplier(cfg, "blast_radius_high", 1.2)
			activeMultipliers = append(activeMultipliers, "blast_radius_high")
		}

		// Confidence threshold filtering (default 0.5 when absent).
		minConfidence, ok := cfg.ConfidenceThresholds[ns]
		if !ok {
			minConfidence = 0.5
		}
		if finding.Confidence < minConfidence {
			continue // Drop low-confidence findings.
		}

		scored = append(scored, schemas.ScoredFinding{
			ID:                fmt.Sprintf("f_%03d", len(scored)),
			DimensionID:       finding.DimensionID,
			DimensionName:     finding.DimensionName,
			FilePath:          finding.FilePath,
			LineStart:         finding.LineStart,
			LineEnd:           finding.LineEnd,
			DiffSide:          "RIGHT", // pydantic default (not set by score_findings)
			Severity:          schemas.Severity(ns),
			Title:             finding.Title,
			Body:              finding.Body,
			Suggestion:        finding.Suggestion,
			Evidence:          finding.Evidence,
			Confidence:        finding.Confidence,
			Tags:              finding.Tags,
			Score:             pyRound(score, 3),
			ActiveMultipliers: activeMultipliers,
		})
	}

	// Add hidden traps from adversary as new findings.
	for _, ar := range adversaryResults {
		if ar.Verdict == "missed_trap" && ar.HiddenTrap != nil && *ar.HiddenTrap != "" {
			scored = append(scored, schemas.ScoredFinding{
				ID:                fmt.Sprintf("f_%03d", len(scored)),
				DimensionID:       "adversary",
				DimensionName:     "Adversary Reviewer",
				FilePath:          "", // Adversary findings may not have specific lines.
				LineStart:         0,
				LineEnd:           0,
				DiffSide:          "RIGHT", // pydantic default
				Severity:          "important",
				Title:             "Hidden trap: " + ar.FindingTitle,
				Body:              *ar.HiddenTrap,
				Confidence:        0.7,
				Tags:              []string{"hidden-trap", "adversary-found"},
				Score:             pyRound(0.7*0.7, 3), // important × 0.7 confidence
				ActiveMultipliers: []string{},
			})
		}
	}

	// Sort by score descending. Python's list.sort is stable and reverse=True
	// keeps equal-scored findings in their original (id-assignment) order, so
	// use sort.SliceStable with a strict-greater-than comparator.
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	return scored
}

// multiplier looks up a named multiplier, falling back to def when absent —
// the Go equivalent of config.multipliers.get(name, def).
func multiplier(cfg config.ScoringConfig, name string, def float64) float64 {
	if v, ok := cfg.Multipliers[name]; ok {
		return v
	}
	return def
}

// DetermineReviewEvent decides the GitHub review event from the merge-gate
// verdict (scoring.py determine_review_event).
//
// Decoupled from severity: the merge gate (the `blocking` flag) is the single
// source of truth for "must fix before merging". Severity remains the reviewer's
// badness label and drives sorting/display, not the event — so a critical-
// severity finding that is not marked blocking does NOT request changes.
//
// Returns one of APPROVE | COMMENT | REQUEST_CHANGES.
func DetermineReviewEvent(findings []schemas.ScoredFinding) string {
	for _, f := range findings {
		if f.Blocking {
			return "REQUEST_CHANGES"
		}
	}
	if len(findings) > 0 {
		return "COMMENT" // Advisory-only findings: surface, but don't gate merge.
	}
	return "APPROVE"
}

// DeduplicateExact removes exact duplicates — same file + same line range + same
// severity (scoring.py deduplicate_exact). This is CODE, not an LLM call; for
// near-duplicates the pipeline uses the DedupGate .ai() call.
func DeduplicateExact(findings []schemas.ReviewFinding) []schemas.ReviewFinding {
	type dedupKey struct {
		filePath  string
		lineStart int
		lineEnd   int
		severity  string
	}

	seen := make(map[dedupKey]struct{}, len(findings))
	deduped := []schemas.ReviewFinding{}

	for _, finding := range findings {
		key := dedupKey{
			filePath:  finding.FilePath,
			lineStart: finding.LineStart,
			lineEnd:   finding.LineEnd,
			severity:  string(finding.Severity),
		}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			deduped = append(deduped, finding)
		}
	}

	return deduped
}
