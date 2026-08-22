// Package orch is the PR-AF review orchestrator — the Go port of
// src/pr_af/orchestrator.py. It coordinates the multi-phase review pipeline:
// intake → anatomy → meta-selectors → review+layer (streaming) → coverage ‖
// consistency → synthesis → merge-gate → output. It owns the HITL revision loop,
// the streaming producer/consumer channel, the order-preserving fan-outs, the
// (inert) wall-clock budget gate, and the byte-exact Markdown output builders.
//
// Concurrency parity: Python's asyncio is cooperative single-threaded, so its
// shared orchestrator state needs no locking. Go runs the fan-outs on real
// goroutines, so every field mutated from a parallel closure (agentInvocations,
// totalCostUSD, costBreakdown, the adversary/cross-ref counters, budgetExhausted)
// is guarded by o.mu.
package orch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/afx"
	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/github"
	"github.com/Agent-Field/pr-af/go/internal/hitl"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// ErrBadInput is the sentinel wrapping every ValueError-class failure Python's
// review() maps to HTTP 400: the "One of pr_url, diff_text, or repo_path is
// required" guard and the _compute_repo_diff failure. The node layer (T4.2)
// maps errors.Is(err, ErrBadInput) to 400 and everything else to 500 with the
// "review execution failed: " prefix.
var ErrBadInput = errors.New("bad input")

// errBudgetExhausted mirrors Python's BudgetExhaustedError (a RuntimeError). It
// is NOT a ValueError, so it maps to 500 — not ErrBadInput.
var errBudgetExhausted = errors.New("budget exhausted")

// errPRDataNotInitialized mirrors Python's RuntimeError("PR data not
// initialized") — a 500-class internal invariant failure.
var errPRDataNotInitialized = errors.New("PR data not initialized")

// App is the agent capability surface the orchestrator depends on. The live
// *agent.Agent satisfies it, and it is the single seam every sub-package's
// narrower interface is fed from: harnessx.HarnessCaller (Harness),
// reasoners.AICaller / gates.AICaller (AI), hitl.Pauser (Pause), hitl.App (Note).
type App interface {
	Harness(ctx context.Context, prompt string, schema map[string]any, dest any, opts harness.Options) (*harness.Result, error)
	AI(ctx context.Context, prompt string, opts ...ai.Option) (*ai.Response, error)
	Pause(ctx context.Context, opts agent.PauseOptions) (*agent.ApprovalResult, error)
	Note(ctx context.Context, message string, tags ...string)
}

// Compile-time proof the live agent satisfies the surface.
var _ App = (*agent.Agent)(nil)

// LocalCaller is the SDK surface for same-process reasoner invocation with
// workflow tracking (Agent.CallLocal): each call builds a child execution
// context from ctx and emits running/succeeded/failed events to the control
// plane, so every pipeline phase renders as its own node in the run's DAG —
// the same orchestration graph the Python port produces via its tracked
// router-reasoner calls. Python's @router.reasoner() wrapper routes direct
// in-process calls through workflow instrumentation; CallLocal is the Go
// SDK's equivalent, and routing the phase seams through it is what keeps the
// Go node from collapsing into a single opaque `review` execution.
type LocalCaller interface {
	CallLocal(ctx context.Context, reasonerName string, input map[string]any) (any, error)
}

// Compile-time proof the live agent satisfies the local-call surface.
var _ LocalCaller = (*agent.Agent)(nil)

// Deps carries the injected capabilities. Divergence from design §C.6: the hax
// client is NOT a Deps field — Python builds it inside run() via
// build_hax_client_from_env (gated on pr_url && !dry_run), so the orchestrator
// mirrors that through the buildHaxClient seam. GH is the GitHub client
// interface (Python constructs GitHubClient() inline; Go injects it so tests can
// stub fetch/post).
type Deps struct {
	App              App
	GH               github.Client
	NodeID           string
	AgentFieldServer string

	// Local, when non-nil, routes every phase invocation through
	// LocalCaller.CallLocal so the control plane records one child execution
	// per phase (the pipeline DAG). nil falls back to plain in-process
	// function calls — the pre-DAG behavior unit tests and stub harnesses
	// rely on. Production (node/register.go) always wires the live agent.
	Local LocalCaller
}

// phaseOrder ports ReviewOrchestrator.PHASE_ORDER — the cost-breakdown /
// phases_completed key list.
var phaseOrder = []string{
	"intake", "anatomy", "meta_selectors", "review",
	"adversary", "cross_ref", "coverage", "synthesis", "output",
}

// Meta-selector configuration (schemas/pipeline.py MetaSelectorConfig — not
// ported to Go config, so bound here).
var enabledLenses = []string{"semantic", "mechanical", "systemic"}
var progressTagValuePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

const (
	adversaryBatchSize = 5
	maxAdversaryBatch  = 4
)

// Orchestrator holds one review's state and seams. New wires the default
// (production) seams; tests override individual fields to stub phases.
type Orchestrator struct {
	deps   Deps
	input  schemas.ReviewInput
	config config.ReviewConfig

	reviewID  string
	startedAt time.Time

	mu                        sync.Mutex // guards the counters below (mutated from fan-out goroutines)
	totalCostUSD              float64
	costBreakdown             map[string]float64
	agentInvocations          int
	budgetExhausted           bool
	durationCapTripped        bool // the wall-clock cap (not the cost cap) exhausted the budget
	reviewDimensionsAttempted int
	reviewDimensionsParseable int
	degradedDimensions        int

	// Single-threaded-written state (set before/after fan-outs, read after joins).
	prData                   *schemas.GitHubPRData
	intakeResult             *schemas.IntakeResult
	anatomyResult            *schemas.AnatomyResult
	metaSelectorResults      []schemas.MetaDimensionResult
	coverageIterations       int
	crossRefCount            int
	adversaryConfirmedCount  int
	adversaryChallengedCount int
	effectiveDepth           string

	patchesCache    []prompts.StrPair
	patchesCacheSet bool

	// clock is time.Since(startedAt) — indirected so budget tests can drive it.
	clock func() time.Duration

	// layerBatchHook, when set, is called with each batch the streaming layer
	// consumer receives — a test seam to prove the layer consumes as reviewers
	// complete (streaming), not after all of them finish (batching). nil in prod.
	layerBatchHook func([]schemas.ReviewFinding)

	// Control-flow seams (default to bound methods; V9 tests override).
	runIntakeFn       func(ctx context.Context) (schemas.IntakeResult, error)
	runAnatomyFn      func(ctx context.Context, intake schemas.IntakeResult) (schemas.AnatomyResult, error)
	resolveDepthFn    func(intake schemas.IntakeResult) string
	runReviewPhasesFn func(ctx context.Context, intake schemas.IntakeResult, anatomy schemas.AnatomyResult, depth, feedback string) (schemas.ReviewPlan, []schemas.ScoredFinding, error)
	generateOutputFn  func(ctx context.Context, scored []schemas.ScoredFinding, intake schemas.IntakeResult, anatomy schemas.AnatomyResult, plan schemas.ReviewPlan, post bool) (schemas.ReviewResult, error)
	cleanupFn         func()
	buildHaxClientFn  func() *hitl.HaxClient
	approvalWebhookFn func() *string
	requestApprovalFn func(ctx context.Context, args hitl.RequestReviewApprovalArgs) hitl.ReviewDecision

	// Reasoner-call seams (default to reasoners.*; streaming/order tests override).
	rfns reasonerSeams
}

type dimensionParseStats struct {
	mu        sync.Mutex
	attempted int
	parseable int
	failed    int
}

type dimensionParseSnapshot struct {
	Attempted int
	Parseable int
	Failed    int
}

func (s *dimensionParseStats) recordAttempt() {
	s.mu.Lock()
	s.attempted++
	s.mu.Unlock()
}

func (s *dimensionParseStats) recordResult(schemaParseFailed bool) {
	s.mu.Lock()
	if schemaParseFailed {
		s.failed++
	} else {
		s.parseable++
	}
	s.mu.Unlock()
}

func (s *dimensionParseStats) snapshot() dimensionParseSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return dimensionParseSnapshot{Attempted: s.attempted, Parseable: s.parseable, Failed: s.failed}
}

func (o *Orchestrator) resetDimensionStats() {
	o.mu.Lock()
	o.reviewDimensionsAttempted = 0
	o.reviewDimensionsParseable = 0
	o.degradedDimensions = 0
	o.mu.Unlock()
}

func (o *Orchestrator) recordDimensionAttempt() {
	o.mu.Lock()
	o.reviewDimensionsAttempted++
	o.mu.Unlock()
}

func (o *Orchestrator) recordDimensionResult(schemaParseFailed bool) {
	o.mu.Lock()
	if schemaParseFailed {
		o.degradedDimensions++
	} else {
		o.reviewDimensionsParseable++
	}
	o.mu.Unlock()
}

func (o *Orchestrator) dimensionStats() dimensionParseSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return dimensionParseSnapshot{Attempted: o.reviewDimensionsAttempted, Parseable: o.reviewDimensionsParseable, Failed: o.degradedDimensions}
}

// reasonerSeams bundles the reasoner entry points the pipeline invokes so tests
// can inject latency/instrumentation without a live harness.
type reasonerSeams struct {
	intake         func(ctx context.Context, deps reasoners.Deps, in reasoners.IntakeInput) (map[string]any, error)
	anatomy        func(ctx context.Context, deps reasoners.Deps, in reasoners.AnatomyInput) (map[string]any, error)
	metaSemantic   func(ctx context.Context, deps reasoners.Deps, in reasoners.MetaInput) (map[string]any, error)
	metaMechanical func(ctx context.Context, deps reasoners.Deps, in reasoners.MetaInput) (map[string]any, error)
	metaSystemic   func(ctx context.Context, deps reasoners.Deps, in reasoners.MetaInput) (map[string]any, error)
	reviewDim      func(ctx context.Context, deps reasoners.Deps, in reasoners.ReviewDimensionInput) (map[string]any, error)
	postWorthiness func(ctx context.Context, deps reasoners.Deps, in reasoners.PostWorthinessInput) (map[string]any, error)
	evidenceVerify func(ctx context.Context, deps reasoners.Deps, in reasoners.EvidenceVerifierInput) (map[string]any, error)
	adversary      func(ctx context.Context, deps reasoners.Deps, in reasoners.AdversaryInput) (map[string]any, error)
	compoundFinder func(ctx context.Context, deps reasoners.Deps, in reasoners.CompoundFinderInput) (map[string]any, error)
	compoundDedup  func(ctx context.Context, deps reasoners.Deps, in reasoners.CompoundDedupInput) (map[string]any, error)
	coverageGate   func(ctx context.Context, deps reasoners.Deps, in reasoners.CoverageGateInput) (map[string]any, error)
	extractOblig   func(ctx context.Context, deps reasoners.Deps, in reasoners.ExtractObligationsInput) (map[string]any, error)
	verifyOblig    func(ctx context.Context, deps reasoners.Deps, in reasoners.VerifyObligationInput) (map[string]any, error)
}

func defaultReasonerSeams() reasonerSeams {
	return reasonerSeams{
		intake:         reasoners.IntakePhase,
		anatomy:        reasoners.AnatomyPhase,
		metaSemantic:   reasoners.MetaSemantic,
		metaMechanical: reasoners.MetaMechanical,
		metaSystemic:   reasoners.MetaSystemic,
		reviewDim:      reasoners.ReviewDimension,
		postWorthiness: reasoners.PostWorthinessGate,
		evidenceVerify: reasoners.EvidenceVerifier,
		adversary:      reasoners.AdversaryPhase,
		compoundFinder: reasoners.CompoundFinderPhase,
		compoundDedup:  reasoners.CompoundDedupPhase,
		coverageGate:   reasoners.CoverageGate,
		extractOblig:   reasoners.ExtractObligations,
		verifyOblig:    reasoners.VerifyObligation,
	}
}

// callLocalSeams routes every phase through local.CallLocal under its
// registered reasoner name (reasoners.Name*), so each invocation is tracked
// as a child execution on the control plane. The registered handler
// (node/register.go) afx.Binds the map back into the same typed input and
// calls the same reasoners.* function with the same Deps the direct seams
// use — behavior is identical, the DAG is the only addition.
func callLocalSeams(local LocalCaller) reasonerSeams {
	return reasonerSeams{
		intake:         viaLocal[reasoners.IntakeInput](local, reasoners.NameIntakePhase),
		anatomy:        viaLocal[reasoners.AnatomyInput](local, reasoners.NameAnatomyPhase),
		metaSemantic:   viaLocal[reasoners.MetaInput](local, reasoners.NameMetaSemantic),
		metaMechanical: viaLocal[reasoners.MetaInput](local, reasoners.NameMetaMechanical),
		metaSystemic:   viaLocal[reasoners.MetaInput](local, reasoners.NameMetaSystemic),
		reviewDim:      viaLocal[reasoners.ReviewDimensionInput](local, reasoners.NameReviewDimension),
		postWorthiness: viaLocal[reasoners.PostWorthinessInput](local, reasoners.NamePostWorthinessGate),
		evidenceVerify: viaLocal[reasoners.EvidenceVerifierInput](local, reasoners.NameEvidenceVerifier),
		adversary:      viaLocal[reasoners.AdversaryInput](local, reasoners.NameAdversaryPhase),
		compoundFinder: viaLocal[reasoners.CompoundFinderInput](local, reasoners.NameCompoundFinderPhase),
		compoundDedup:  viaLocal[reasoners.CompoundDedupInput](local, reasoners.NameCompoundDedupPhase),
		coverageGate:   viaLocal[reasoners.CoverageGateInput](local, reasoners.NameCoverageGate),
		extractOblig:   viaLocal[reasoners.ExtractObligationsInput](local, reasoners.NameExtractObligations),
		verifyOblig:    viaLocal[reasoners.VerifyObligationInput](local, reasoners.NameVerifyObligation),
	}
}

// viaLocal adapts one typed seam to a CallLocal invocation: typed input →
// afx.ToMap → CallLocal(name) → registered handler (afx.Bind → reasoners.*).
// The reasoners.Deps parameter is ignored — the handler closes over the
// node-level Deps built at registration, which point at the same live agent.
// Handlers return map[string]any; a nil result (impossible today: every
// reasoner returns a non-nil map or an error) surfaces as an empty map so
// callers keep their raw-map contract.
func viaLocal[T any](local LocalCaller, name string) func(context.Context, reasoners.Deps, T) (map[string]any, error) {
	return func(ctx context.Context, _ reasoners.Deps, in T) (map[string]any, error) {
		input, err := afx.ToMap(in)
		if err != nil {
			return nil, err
		}
		raw, err := local.CallLocal(ctx, name, input)
		if err != nil {
			return nil, err
		}
		out, ok := raw.(map[string]any)
		if !ok {
			if raw == nil {
				return map[string]any{}, nil
			}
			return nil, fmt.Errorf("orch: reasoner %s returned %T, want map[string]any", name, raw)
		}
		return out, nil
	}
}

// New constructs an Orchestrator with production seams. startedAt is set now so
// the wall-clock budget gate measures from construction (parity with Python's
// time.monotonic() in __init__).
func New(d Deps, in schemas.ReviewInput, cfg config.ReviewConfig) *Orchestrator {
	o := &Orchestrator{
		deps:           d,
		input:          in,
		config:         cfg,
		reviewID:       "rev_" + hex12(),
		startedAt:      time.Now(),
		costBreakdown:  make(map[string]float64, len(phaseOrder)),
		effectiveDepth: "standard",
		rfns:           defaultReasonerSeams(),
	}
	for _, p := range phaseOrder {
		o.costBreakdown[p] = 0.0
	}
	if d.Local != nil {
		o.rfns = callLocalSeams(d.Local)
	}
	o.clock = func() time.Duration { return time.Since(o.startedAt) }

	o.runIntakeFn = o.runIntake
	o.runAnatomyFn = o.runAnatomy
	o.resolveDepthFn = o.resolveDepth
	o.runReviewPhasesFn = o.runReviewPhases
	o.generateOutputFn = o.generateOutput
	o.cleanupFn = o.cleanupContextDir
	o.buildHaxClientFn = hitl.BuildHaxClientFromEnv
	o.approvalWebhookFn = func() *string { return hitl.ApprovalWebhookURL(d.AgentFieldServer) }
	o.requestApprovalFn = hitl.RequestReviewApproval
	return o
}

// reasonerDeps builds the reasoner capability bundle from the single App seam.
func (o *Orchestrator) reasonerDeps() reasoners.Deps {
	return reasoners.Deps{Harness: o.deps.App, AI: o.deps.App}
}

// hex12 ports uuid4().hex[:12] — 6 random bytes rendered as 12 lowercase hex.
func hex12() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Run executes the pipeline with the HITL revision loop (orchestrator.py run()).
// ErrBadInput-wrapped errors map to 400 at the node; anything else to 500.
func (o *Orchestrator) Run(ctx context.Context) (schemas.ReviewResult, error) {
	var zero schemas.ReviewResult
	o.noteProgress(ctx, "intake", "started", "Review intake started")

	intake, err := o.runIntakeFn(ctx)
	if err != nil {
		return zero, err
	}
	o.intakeResult = &intake
	reviewDepth := o.resolveDepthFn(intake)
	o.noteProgress(ctx, "intake", "completed", "Review intake completed")

	o.noteProgress(ctx, "anatomy", "started", "Repository anatomy started")
	anatomy, err := o.runAnatomyFn(ctx, intake)
	if err != nil {
		return zero, err
	}
	o.anatomyResult = &anatomy
	o.noteProgress(ctx, "anatomy", "completed", "Repository anatomy completed")

	// HITL gate active only when there is a real PR to post to and we are not in
	// dry-run — otherwise hax is nil and we post directly, exactly as before.
	var haxClient *hitl.HaxClient
	if strp(o.input.PrURL) != "" && !o.input.DryRun {
		haxClient = o.buildHaxClientFn()
	}
	maxRevisions := 0
	if haxClient != nil {
		maxRevisions = o.config.HITL.MaxReviewRevisions
	}
	revisionHistory := []string{}
	reviewerFeedback := ""

	for revisionIter := 0; revisionIter <= maxRevisions; revisionIter++ {
		o.resetDimensionStats()
		o.noteProgress(ctx, "review", "started", "Review analysis started")
		plan, scored, err := o.runReviewPhasesFn(ctx, intake, anatomy, reviewDepth, reviewerFeedback)
		if err != nil {
			return zero, err
		}
		o.noteProgress(ctx, "review", "completed", fmt.Sprintf("Review analysis completed with %d findings", len(scored)))

		if haxClient == nil {
			return o.finish(ctx, scored, intake, anatomy, plan, true)
		}

		// Nothing to triage: don't bother a human, and don't auto-post an empty
		// "approved" review to a public repo. Complete silently.
		if len(scored) == 0 {
			o.deps.App.Note(ctx, "hitl: no findings — skipping review gate, posting nothing",
				"hitl", "no-post", "no-findings")
			return o.finish(ctx, scored, intake, anatomy, plan, false)
		}

		decision := o.requestApprovalFn(ctx, hitl.RequestReviewApprovalArgs{
			App:             o.deps.App,
			Pauser:          o.deps.App,
			HaxClient:       haxClient,
			PRIntent:        intake.PrSummary,
			Findings:        scored,
			PRLabel:         o.prLabel(),
			WebhookURL:      o.approvalWebhookFn(),
			UserID:          o.config.HITL.ApprovalUserID,
			ExpiresInHours:  o.config.HITL.ApprovalExpiresInHours,
			PRMeta:          o.prMeta(),
			RevisionIter:    revisionIter,
			RevisionHistory: revisionHistory,
			Metadata:        o.hitlMetadata(ctx),
		})

		if decision.IsPost() {
			approved := make([]schemas.ScoredFinding, 0, len(scored))
			for _, f := range scored {
				if _, ok := decision.SelectedFindingIDs[f.ID]; ok {
					approved = append(approved, f)
				}
			}
			return o.finish(ctx, approved, intake, anatomy, plan, true)
		}

		if decision.IsRerun() && revisionIter < maxRevisions {
			revisionHistory = append(revisionHistory, decision.Instructions)
			reviewerFeedback = o.mergeFeedback(revisionHistory)
			continue
		}

		// reject, expire/error, or a re-review past the revision cap → no post.
		reason := decision.Action
		if decision.IsRerun() {
			reason = "revision cap reached"
		}
		detail := strings.TrimSpace(decision.Instructions)
		detailSuffix := ""
		if detail != "" {
			detailSuffix = ": " + detail
		}
		o.deps.App.Note(ctx,
			fmt.Sprintf("hitl: not posting review (%s; raw=%s)%s", reason, decision.DecisionRaw, detailSuffix),
			"hitl", "no-post", decision.Action)
		return o.finish(ctx, scored, intake, anatomy, plan, false)
	}

	return zero, errors.New("review loop exited without producing a result")
}

// noteProgress emits readable messages plus searchable stage/status tags for
// callers and AgentField user interfaces that render execution notes.
func (o *Orchestrator) noteProgress(ctx context.Context, stage, status, message string) {
	tags := []string{"pr-af-progress", "stage:" + stage, "status:" + status}
	if jobID := strings.TrimSpace(o.input.PublisherJobID); progressTagValuePattern.MatchString(jobID) {
		tags = append(tags, "job:"+jobID)
	}
	if pullRequest := o.input.PullRequest; pullRequest != nil {
		if pullRequest.Number > 0 {
			tags = append(tags, fmt.Sprintf("pr:%d", pullRequest.Number))
		}
		if stage == "intake" && status == "started" {
			identity := strings.TrimSpace(pullRequest.Title)
			if pullRequest.Number > 0 {
				identity = fmt.Sprintf("#%d — %s", pullRequest.Number, identity)
			}
			if identity != "" {
				message += " for " + strings.TrimSpace(identity)
			}
			if pullRequest.URL != "" {
				message += " (" + pullRequest.URL + ")"
			}
		}
	}
	o.deps.App.Note(ctx, message, tags...)
}

// finish generates output (optionally posting) and cleans up the context dir.
func (o *Orchestrator) finish(
	ctx context.Context,
	scored []schemas.ScoredFinding,
	intake schemas.IntakeResult,
	anatomy schemas.AnatomyResult,
	plan schemas.ReviewPlan,
	post bool,
) (schemas.ReviewResult, error) {
	result, err := o.generateOutputFn(ctx, scored, intake, anatomy, plan, post)
	if err != nil {
		return schemas.ReviewResult{}, err
	}
	o.cleanupFn()
	return result, nil
}

// ---- HITL helper blocks (camelCase surfaces, design §B.5) ----

func (o *Orchestrator) prLabel() string {
	pr := o.prData
	if pr != nil && pr.Owner != "" && pr.Repo != "" {
		return fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	}
	return ""
}

// prMeta ports _pr_meta — the camelCase PR block for the hax template.
func (o *Orchestrator) prMeta() map[string]any {
	pr := o.prData
	if pr == nil {
		return map[string]any{}
	}
	url := strp(o.input.PrURL)
	if url == "" && pr.Owner != "" && pr.Repo != "" && pr.Number != 0 {
		url = fmt.Sprintf("https://github.com/%s/%s/pull/%d", pr.Owner, pr.Repo, pr.Number)
	}
	repo := ""
	if pr.Owner != "" && pr.Repo != "" {
		repo = pr.Owner + "/" + pr.Repo
	} else {
		repo = pr.Repo
	}
	meta := map[string]any{
		"title":  pr.Title,
		"number": prNumberOrNil(pr.Number),
		"url":    url,
		"repo":   repo,
		"author": pr.Author,
	}
	if len(pr.ChangedFiles) > 0 {
		additions, deletions := 0, 0
		for _, f := range pr.ChangedFiles {
			additions += f.Additions
			deletions += f.Deletions
		}
		meta["filesChangedCount"] = len(pr.ChangedFiles)
		meta["additionsCount"] = additions
		meta["deletionsCount"] = deletions
	}
	return meta
}

// prNumberOrNil mirrors `pr.number or None`: 0 → nil (JSON null).
func prNumberOrNil(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// mergeFeedback ports _merge_feedback — collapse accumulated instructions with
// " | ", trimming empties.
func (o *Orchestrator) mergeFeedback(revisionHistory []string) string {
	items := make([]string, 0, len(revisionHistory))
	for _, instr := range revisionHistory {
		t := strings.TrimSpace(instr)
		if t != "" {
			items = append(items, t)
		}
	}
	return strings.Join(items, " | ")
}

// hitlMetadata ports _hitl_metadata (camelCase). executionId comes from the
// SDK's exported execution-context accessor — an empty string when the ctx
// carries no execution (matching Python's fallback when app.ctx is None).
func (o *Orchestrator) hitlMetadata(ctx context.Context) map[string]any {
	return map[string]any{
		"prLabel":     o.prLabel(),
		"prUrl":       strp(o.input.PrURL),
		"reviewId":    o.reviewID,
		"executionId": agent.ExecutionContextFrom(ctx).ExecutionID,
	}
}

// ---- budget / cost (inert cost, live wall-clock) ----

// budgetOrTimeoutExhausted ports _budget_or_timeout_exhausted. Cost stays 0
// (reasoner returns never carry cost_usd), so only the wall-clock check trips.
// Which cap tripped is recorded so budgetExhaustedMessage can word the failure
// honestly (§B.4 pair with Python's _budget_exhausted_message).
func (o *Orchestrator) budgetOrTimeoutExhausted(phase string) bool {
	elapsed := o.clock().Seconds()
	o.mu.Lock()
	defer o.mu.Unlock()
	if elapsed > float64(o.config.Budget.MaxDurationSeconds) {
		o.budgetExhausted = true
		o.durationCapTripped = true
		return true
	}
	if o.totalCostUSD >= o.config.Budget.MaxCostUSD {
		o.budgetExhausted = true
		return true
	}
	phaseSpent := o.costBreakdown[phase]
	phaseCap, ok := o.config.Budget.PhaseBudgets[phase]
	if !ok {
		return false // absent phase → cap is +inf → never trips
	}
	return phaseSpent >= phaseCap
}

// budgetExhaustedMessage words the exhaustion by cause: the wall-clock cap gets
// an explicit timeout message (in the Go port cost never accrues, so this is
// the only cap that actually trips), the cost cap keeps the historical
// "Budget exhausted before <phase>" wording. Byte-identical to Python's
// _budget_exhausted_message (§B.4).
func (o *Orchestrator) budgetExhaustedMessage(phase string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.durationCapTripped {
		return fmt.Sprintf("Review time budget exceeded (max_duration_seconds=%d) before %s",
			o.config.Budget.MaxDurationSeconds, phase)
	}
	return "Budget exhausted before " + phase
}

// registerCost ports _register_cost∘_extract_cost. extractCost reads "cost_usd"
// off the reasoner return (always absent), so total stays 0.0.
func (o *Orchestrator) registerCost(phase string, resultRaw map[string]any) {
	cost, ok := extractCost(resultRaw)
	if !ok {
		return
	}
	o.mu.Lock()
	o.totalCostUSD += cost
	o.costBreakdown[phase] += cost
	o.mu.Unlock()
}

func (o *Orchestrator) incInvocations(n int) {
	o.mu.Lock()
	o.agentInvocations += n
	o.mu.Unlock()
}

func (o *Orchestrator) invocations() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.agentInvocations
}

func (o *Orchestrator) isBudgetExhausted() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.budgetExhausted
}

func (o *Orchestrator) totalCost() float64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.totalCostUSD
}

// extractCost ports _extract_cost: reads cost_usd off the raw map or its
// unwrapped payload. Always returns (0, false) in practice.
func extractCost(resultRaw map[string]any) (float64, bool) {
	if resultRaw == nil {
		return 0, false
	}
	if c, ok := asFloat(resultRaw["cost_usd"]); ok {
		return c, true
	}
	payload := unwrap(resultRaw)
	if payload != nil {
		if c, ok := asFloat(payload["cost_usd"]); ok {
			return c, true
		}
	}
	return 0, false
}

// resolveDepth ports _resolve_depth.
func (o *Orchestrator) resolveDepth(intake schemas.IntakeResult) string {
	if o.input.Depth != "auto" {
		return o.input.Depth
	}
	if _, ok := config.DepthProfiles[intake.ReviewDepth]; ok {
		return intake.ReviewDepth
	}
	if o.prData != nil && o.prData.Diff != "" {
		lineCount := len(strings.Split(o.prData.Diff, "\n"))
		// Python splitlines() does not count a trailing newline as an extra line;
		// mirror it.
		lineCount = countSplitlines(o.prData.Diff)
		// Under the smallest threshold → that threshold's depth.
		if len(config.AutoDepthThresholds) > 0 {
			minTh := config.AutoDepthThresholds[0]
			for _, th := range config.AutoDepthThresholds {
				if th.Lines < minTh.Lines {
					minTh = th
				}
			}
			if lineCount < minTh.Lines {
				return minTh.Depth
			}
			// Ascending scan: first threshold the count is under.
			for _, th := range config.AutoDepthThresholds {
				if lineCount < th.Lines {
					return th.Depth
				}
			}
		}
		return "deep"
	}
	return "standard"
}

// escalateDepth ports _escalate_depth.
func (o *Orchestrator) escalateDepth(currentDepth string) string {
	if currentDepth == "deep" {
		return "deep"
	}
	signals := 0
	if o.anatomyResult != nil {
		if len(o.anatomyResult.BlastRadius) > 10 {
			signals++
		}
		if len(o.anatomyResult.IntentGaps) > 0 {
			signals++
		}
		if len(o.anatomyResult.RiskSurfaces) > 3 {
			signals++
		}
		if o.anatomyResult.Stats.TotalAdditions > 500 {
			signals++
		}
	}
	if len(o.metaSelectorResults) > 0 {
		low := 0
		for _, m := range o.metaSelectorResults {
			if m.Confidence < 0.5 {
				low++
			}
		}
		if low >= 2 {
			signals++
		}
	}
	if signals >= 2 && currentDepth == "quick" {
		return "standard"
	}
	if signals >= 3 && currentDepth == "standard" {
		return "deep"
	}
	return currentDepth
}
