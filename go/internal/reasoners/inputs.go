package reasoners

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// This file declares the typed inputs for each reasoner (design §C.6 "typed
// input equivalent"). Field names and json tags are the exact Python keyword
// parameter names, so node/register.go can afx.Bind a CP request body straight
// into them; structs whose Python signature carries non-zero defaults seed
// them in UnmarshalJSON. The orchestrator constructs these values directly
// for the in-process calls.

// OrderedPatches is a diff_patches dict with preserved insertion order —
// Python's dict[str, str] whose ordering the meta/deepen/obligation prompts
// render. It JSON-round-trips as an object, decoding in document order.
type OrderedPatches []prompts.StrPair

// UnmarshalJSON decodes a JSON object into ordered pairs, preserving the
// document's key order (duplicate keys: last value wins, first position kept —
// matching Python's dict literal semantics). null decodes to nil.
func (p *OrderedPatches) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil { // JSON null
		*p = nil
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("reasoners: diff_patches must be an object, got %v", tok)
	}
	out := OrderedPatches{}
	idx := map[string]int{}
	for dec.More() {
		kTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := kTok.(string)
		if !ok {
			return fmt.Errorf("reasoners: diff_patches key %v is not a string", kTok)
		}
		var val string
		if err := dec.Decode(&val); err != nil {
			return fmt.Errorf("reasoners: diff_patches[%q]: %w", key, err)
		}
		if i, dup := idx[key]; dup {
			out[i].Val = val
			continue
		}
		idx[key] = len(out)
		out = append(out, prompts.StrPair{Key: key, Val: val})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return err
	}
	*p = out
	return nil
}

// MarshalJSON renders the pairs as a JSON object in slice order.
func (p OrderedPatches) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, pair := range p {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(pair.Key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(pair.Val)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// IntakeInput mirrors intake_phase(pr_data, depth="standard").
type IntakeInput struct {
	PRData schemas.GitHubPRData `json:"pr_data"`
	Depth  string               `json:"depth"`
}

func (i *IntakeInput) UnmarshalJSON(b []byte) error {
	*i = IntakeInput{Depth: "standard"}
	type alias IntakeInput
	return json.Unmarshal(b, (*alias)(i))
}

// AnatomyInput mirrors anatomy_phase(pr_data, intake, repo_path="").
type AnatomyInput struct {
	PRData   schemas.GitHubPRData `json:"pr_data"`
	Intake   schemas.IntakeResult `json:"intake"`
	RepoPath string               `json:"repo_path"`
}

// PlanningInput mirrors planning_phase(intake, anatomy, depth="standard",
// hints=None).
type PlanningInput struct {
	Intake  schemas.IntakeResult  `json:"intake"`
	Anatomy schemas.AnatomyResult `json:"anatomy"`
	Depth   string                `json:"depth"`
	Hints   []string              `json:"hints"`
}

func (p *PlanningInput) UnmarshalJSON(b []byte) error {
	*p = PlanningInput{Depth: "standard"}
	type alias PlanningInput
	return json.Unmarshal(b, (*alias)(p))
}

// MetaInput mirrors the shared signature of meta_semantic / meta_mechanical /
// meta_systemic(intake, anatomy, depth="standard", repo_path="",
// diff_patches=None, reviewer_feedback="").
type MetaInput struct {
	Intake           schemas.IntakeResult  `json:"intake"`
	Anatomy          schemas.AnatomyResult `json:"anatomy"`
	Depth            string                `json:"depth"`
	RepoPath         string                `json:"repo_path"`
	DiffPatches      OrderedPatches        `json:"diff_patches"`
	ReviewerFeedback string                `json:"reviewer_feedback"`
}

func (m *MetaInput) UnmarshalJSON(b []byte) error {
	*m = MetaInput{Depth: "standard"}
	type alias MetaInput
	return json.Unmarshal(b, (*alias)(m))
}

// ReviewDimensionInput mirrors review_dimension's long keyword signature.
// DiffPatches is a plain map — the prompt renders patches in TargetFiles
// order, so dict ordering is immaterial here.
type ReviewDimensionInput struct {
	ReviewPrompt      string            `json:"review_prompt"`
	TargetFiles       []string          `json:"target_files"`
	ContextFiles      []string          `json:"context_files"`
	RepoPath          string            `json:"repo_path"`
	CurrentDepth      int               `json:"current_depth"`
	MaxDepth          int               `json:"max_depth"`
	PrNarrative       string            `json:"pr_narrative"`
	RiskSurfaces      []string          `json:"risk_surfaces"`
	IntakeSummary     string            `json:"intake_summary"`
	PrDescription     string            `json:"pr_description"`
	DiffPatches       map[string]string `json:"diff_patches"`
	AllDimensionNames []string          `json:"all_dimension_names"`
	ReviewerFeedback  string            `json:"reviewer_feedback"`
	PrimedCode        string            `json:"primed_code"`
}

func (r *ReviewDimensionInput) UnmarshalJSON(b []byte) error {
	*r = ReviewDimensionInput{MaxDepth: 2}
	type alias ReviewDimensionInput
	return json.Unmarshal(b, (*alias)(r))
}

// CompoundFinderInput mirrors compound_finder_phase(cluster_findings,
// repo_path="", evidence_map=None). EvidenceMap values are
// EvidencePackage.model_dump()-shaped dicts keyed by finding title.
type CompoundFinderInput struct {
	ClusterFindings []schemas.ReviewFinding   `json:"cluster_findings"`
	RepoPath        string                    `json:"repo_path"`
	EvidenceMap     map[string]map[string]any `json:"evidence_map"`
}

// PostWorthinessInput mirrors post_worthiness_gate(findings). The live path
// passes ReviewFinding dumps.
type PostWorthinessInput struct {
	Findings []schemas.ReviewFinding `json:"findings"`
}

// CompoundDedupInput mirrors compound_dedup_phase(compound_findings,
// individual_findings_summary="").
type CompoundDedupInput struct {
	CompoundFindings          []schemas.ReviewFinding `json:"compound_findings"`
	IndividualFindingsSummary string                  `json:"individual_findings_summary"`
}

// EvidenceVerifierInput mirrors evidence_verifier(findings,
// evidence_packages=None, pr_context="", repo_path="").
type EvidenceVerifierInput struct {
	Findings         []schemas.ReviewFinding   `json:"findings"`
	EvidencePackages map[string]map[string]any `json:"evidence_packages"`
	PrContext        string                    `json:"pr_context"`
	RepoPath         string                    `json:"repo_path"`
}

// AdversaryInput mirrors adversary_phase(findings, ai_generated_confidence=0.0,
// pr_context="", repo_path="", evidence_packages=None).
type AdversaryInput struct {
	Findings              []schemas.ReviewFinding   `json:"findings"`
	AIGeneratedConfidence float64                   `json:"ai_generated_confidence"`
	PrContext             string                    `json:"pr_context"`
	RepoPath              string                    `json:"repo_path"`
	EvidencePackages      map[string]map[string]any `json:"evidence_packages"`
}

// DeepenInput mirrors deepen_findings(diff_patches=None, existing_titles=None,
// repo_path="", pr_context="").
type DeepenInput struct {
	DiffPatches    OrderedPatches `json:"diff_patches"`
	ExistingTitles []string       `json:"existing_titles"`
	RepoPath       string         `json:"repo_path"`
	PrContext      string         `json:"pr_context"`
}

// ExtractObligationsInput mirrors extract_obligations(diff_patches=None,
// repo_path="", pr_context="").
type ExtractObligationsInput struct {
	DiffPatches OrderedPatches `json:"diff_patches"`
	RepoPath    string         `json:"repo_path"`
	PrContext   string         `json:"pr_context"`
}

// VerifyObligationInput mirrors verify_obligation(obligation, repo_path="").
// Obligation is the raw dict (an ExtractObligations output element); the
// reasoner validates it into the private obligation struct exactly as Python
// runs _Obligation.model_validate.
type VerifyObligationInput struct {
	Obligation map[string]any `json:"obligation"`
	RepoPath   string         `json:"repo_path"`
}

// CoverageGateInput mirrors coverage_gate(anatomy, reviewed_clusters,
// dimension_names_reviewed=None).
type CoverageGateInput struct {
	Anatomy                schemas.AnatomyResult `json:"anatomy"`
	ReviewedClusters       []string              `json:"reviewed_clusters"`
	DimensionNamesReviewed []string              `json:"dimension_names_reviewed"`
}
