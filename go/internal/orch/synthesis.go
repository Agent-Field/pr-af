package orch

// synthesis.go ports Phase 7 (_synthesize): deterministic dedup → scoring →
// truncate to max_comments.

import (
	"github.com/Agent-Field/pr-af/go/internal/schemas"
	"github.com/Agent-Field/pr-af/go/internal/scoring"
)

func (o *Orchestrator) synthesize(
	findings []schemas.ReviewFinding,
	adversaryResults []schemas.AdversaryResult,
) []schemas.ScoredFinding {
	deduped := scoring.DeduplicateExact(findings)
	aiGen := 0.0
	if o.intakeResult != nil {
		aiGen = o.intakeResult.AIGenerated
	}
	blastSize := 0
	if o.anatomyResult != nil {
		blastSize = len(o.anatomyResult.BlastRadius)
	}
	scored := scoring.ScoreFindings(deduped, adversaryResults, o.config.Scoring, aiGen, blastSize)
	if len(scored) > o.config.Comments.MaxComments {
		scored = scored[:o.config.Comments.MaxComments]
	}
	return scored
}
