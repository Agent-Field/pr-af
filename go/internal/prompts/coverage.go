package prompts

import "github.com/Agent-Field/pr-af/go/internal/schemas"

// Ports the coverage_gate .ai() system + user prompt from
// reasoners/harnesses.py.

// CoverageGateSystem is the system prompt for the coverage_gate .ai() call.
const CoverageGateSystem = "Analyze the coverage state and return the structured result."

// CoverageGatePrompt builds the coverage_gate user prompt. The cluster payload
// here is keyed id, name, description, files (no primary_language — distinct
// from _cluster_descriptions).
func CoverageGatePrompt(anatomy schemas.AnatomyResult, reviewedClusters, dimensionNamesReviewed []string) string {
	clusters := make([]*OMap, len(anatomy.Clusters))
	for i, c := range anatomy.Clusters {
		clusters[i] = omap(
			"id", c.ID,
			"name", c.Name,
			"description", c.Description,
			"files", orEmpty(c.Files),
		)
	}
	ctx := omap(
		"all_clusters", clusters,
		"reviewed_clusters", orEmpty(reviewedClusters),
		"dimensions_reviewed", orEmpty(dimensionNamesReviewed),
		"risk_surfaces", orEmpty(anatomy.RiskSurfaces),
	)
	return "Determine whether review coverage is complete. " +
		"Compare reviewed cluster identifiers against all change clusters. " +
		"Dimensions already reviewed: " + joinComma(orEmpty(dimensionNamesReviewed)) + ". " +
		"If gaps exist, return concise gap_descriptions.\n\n" + pyJSON(ctx)
}
