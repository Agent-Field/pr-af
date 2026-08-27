package reasoners

import (
	"context"
	"unicode/utf8"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/harnessx"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
)

// EvidenceVerifier ports evidence_verifier: independent verification of
// critical/important findings against programmatically extracted ground-truth
// code, before the adversarial phase.
//
// Output keys (§B.2): verified_findings. Missing or unparseable structured
// output fails verification rather than erasing evidence.
func EvidenceVerifier(ctx context.Context, deps Deps, in EvidenceVerifierInput) (map[string]any, error) {
	evMap := evidenceOMaps(in.EvidencePackages)

	// The builder embeds a file reference when the findings JSON exceeds 12000
	// characters and a repo path exists; the write is the reasoner's job.
	findingsText := evidenceVerifierContext(in.Findings, evMap)
	if utf8.RuneCountInString(findingsText) > 12000 && in.RepoPath != "" {
		if _, err := writeContextFile(findingsText, "verification_findings.json", in.RepoPath); err != nil {
			return nil, err
		}
	}

	prompt := prompts.EvidenceVerifierPrompt(in.Findings, evMap, in.PrContext, in.RepoPath)
	parsed, _, err := harnessx.Run[verificationResult](ctx, deps.Harness, prompt, harness.Options{Cwd: in.RepoPath})
	if err != nil {
		return nil, err
	}
	verified, err := dumpSlice(parsed.VerifiedFindings)
	if err != nil {
		return nil, err
	}
	return map[string]any{"verified_findings": verified}, nil
}
