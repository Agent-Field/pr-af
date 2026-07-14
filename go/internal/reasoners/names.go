package reasoners

// Control-plane registration names of the 16 router reasoners (design §B.1).
// node/register.go registers each handler under these names, and the
// orchestrator's CallLocal-routed seams (orch.callLocalSeams) invoke them by
// the same constants — a single source of truth so the DAG's phase names can
// never drift from the registered surface.
const (
	NameIntakePhase         = "intake_phase"
	NameAnatomyPhase        = "anatomy_phase"
	NamePlanningPhase       = "planning_phase"
	NameMetaSemantic        = "meta_semantic"
	NameMetaMechanical      = "meta_mechanical"
	NameMetaSystemic        = "meta_systemic"
	NameReviewDimension     = "review_dimension"
	NameCompoundFinderPhase = "compound_finder_phase"
	NamePostWorthinessGate  = "post_worthiness_gate"
	NameCompoundDedupPhase  = "compound_dedup_phase"
	NameEvidenceVerifier    = "evidence_verifier"
	NameAdversaryPhase      = "adversary_phase"
	NameDeepenFindings      = "deepen_findings"
	NameExtractObligations  = "extract_obligations"
	NameVerifyObligation    = "verify_obligation"
	NameCoverageGate        = "coverage_gate"
)
