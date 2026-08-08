package orch

// phases.go ports the finding-producing phases of orchestrator.py: intake,
// anatomy, meta-selectors, the streaming review+layer, evidence verification,
// parallel adversary, coverage loop, and consistency verify. The concurrency
// primitives map (design §D): asyncio.gather → order-preserving errgroup with a
// pre-indexed result slice; asyncio.Semaphore → semaphore.Weighted; the
// streaming asyncio.Queue → an (unbuffered) chan []ReviewFinding with the
// producer closing on completion and the consumer ranging it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/diffengine"
	"github.com/Agent-Field/pr-af/go/internal/evidence"
	"github.com/Agent-Field/pr-af/go/internal/gates"
	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// ---- Phase 1: INTAKE ----

func (o *Orchestrator) runIntake(ctx context.Context) (schemas.IntakeResult, error) {
	if o.budgetOrTimeoutExhausted("intake") {
		return schemas.IntakeResult{}, budgetExhaustedErr(o.budgetExhaustedMessage("intake"))
	}

	switch {
	case strp(o.input.PrURL) != "":
		owner, repo, number, err := o.deps.GH.ParsePRURL(strp(o.input.PrURL))
		if err != nil {
			return schemas.IntakeResult{}, badInput(err.Error())
		}
		prData, err := o.deps.GH.FetchPR(ctx, owner, repo, number)
		if err != nil {
			return schemas.IntakeResult{}, err
		}
		o.prData = &prData
	case strp(o.input.DiffText) != "":
		diff := strp(o.input.DiffText)
		parsed := diffengine.ParseUnifiedDiff(diff)
		o.prData = &schemas.GitHubPRData{
			Title:        "Local Diff Review",
			Diff:         diff,
			ChangedFiles: toChangedFiles(parsed),
		}
	case strp(o.input.RepoPath) != "":
		diff, err := computeRepoDiff(ctx, strp(o.input.RepoPath), strp(o.input.BaseRef), strp(o.input.HeadRef))
		if err != nil {
			return schemas.IntakeResult{}, err
		}
		parsed := diffengine.ParseUnifiedDiff(diff)
		number := 0
		if o.input.PostPRNumber != nil {
			number = *o.input.PostPRNumber
		}
		o.prData = &schemas.GitHubPRData{
			Number:       number,
			Title:        "Local Repository Review",
			Diff:         diff,
			ChangedFiles: toChangedFiles(parsed),
		}
	default:
		return schemas.IntakeResult{}, badInput("One of pr_url, diff_text, or repo_path is required")
	}

	resultRaw, err := o.rfns.intake(ctx, o.reasonerDeps(), reasoners.IntakeInput{
		PRData: *o.prData,
		Depth:  o.input.Depth,
	})
	if err != nil {
		return schemas.IntakeResult{}, err
	}
	o.incInvocations(1)
	o.registerCost("intake", resultRaw)
	return mapToStruct[schemas.IntakeResult](resultRaw), nil
}

// ---- Phase 2: ANATOMY ----

func (o *Orchestrator) runAnatomy(ctx context.Context, intake schemas.IntakeResult) (schemas.AnatomyResult, error) {
	if o.budgetOrTimeoutExhausted("anatomy") {
		return schemas.AnatomyResult{}, budgetExhaustedErr(o.budgetExhaustedMessage("anatomy"))
	}
	if o.prData == nil {
		return schemas.AnatomyResult{}, errPRDataNotInitialized
	}
	resultRaw, err := o.rfns.anatomy(ctx, o.reasonerDeps(), reasoners.AnatomyInput{
		PRData:   *o.prData,
		Intake:   intake,
		RepoPath: strp(o.input.RepoPath),
	})
	if err != nil {
		return schemas.AnatomyResult{}, err
	}
	o.incInvocations(1)
	o.registerCost("anatomy", resultRaw)
	return mapToStruct[schemas.AnatomyResult](resultRaw), nil
}

// ---- runReviewPhases: meta-selectors → review+layer → coverage ‖ consistency → synthesize → merge-gate ----

func (o *Orchestrator) runReviewPhases(
	ctx context.Context,
	intake schemas.IntakeResult,
	anatomy schemas.AnatomyResult,
	reviewDepth, reviewerFeedback string,
) (schemas.ReviewPlan, []schemas.ScoredFinding, error) {
	plan, err := o.runMetaSelectors(ctx, intake, anatomy, reviewDepth, reviewerFeedback)
	if err != nil {
		return schemas.ReviewPlan{}, nil, err
	}

	var allFindings []schemas.ReviewFinding
	var adversaryResults []schemas.AdversaryResult

	if o.config.Comments.PostWorthinessGate {
		// EARLY-GATE reorder: run reviewers to completion, gate, then heavy layer.
		reviewerFindings, err := o.collectParallelReview(ctx, plan, reviewerFeedback)
		if err != nil {
			return schemas.ReviewPlan{}, nil, err
		}
		kept := reviewerFindings
		if len(reviewerFindings) > 1 {
			if sel, ok := o.postWorthinessSelect(ctx, reviewerFindings); ok && len(sel) > 0 {
				kept = sel
			}
		}
		lq := make(chan []schemas.ReviewFinding, 1)
		if len(kept) > 0 {
			lq <- kept
		}
		close(lq)
		allFindings, adversaryResults, err = o.runReviewLayer(ctx, plan, lq, anatomy)
		if err != nil {
			return schemas.ReviewPlan{}, nil, err
		}
	} else {
		// Gate OFF (default): review and layer stream through one channel.
		var err error
		allFindings, adversaryResults, err = o.streamReviewLayer(ctx, plan, anatomy, reviewerFeedback)
		if err != nil {
			return schemas.ReviewPlan{}, nil, err
		}
	}

	// Phase 6 (coverage) ‖ Phase 6.7 (consistency) — independent, run concurrently.
	covFindings, covAdversary, cvFindings, err := o.runCoverageAndConsistency(ctx, plan, anatomy, allFindings, adversaryResults)
	if err != nil {
		return schemas.ReviewPlan{}, nil, err
	}
	adversaryResults = covAdversary

	challenged, confirmed := 0, 0
	for _, r := range adversaryResults {
		if r.Verdict == "challenged" {
			challenged++
		}
		if r.Verdict == "confirmed" {
			confirmed++
		}
	}
	o.adversaryChallengedCount = challenged
	o.adversaryConfirmedCount = confirmed

	var consistencyNew []schemas.ReviewFinding
	for _, f := range cvFindings {
		if f.DimensionID == "consistency-verify" {
			consistencyNew = append(consistencyNew, f)
		}
	}
	merged := append(append([]schemas.ReviewFinding(nil), covFindings...), consistencyNew...)

	scored := o.synthesize(merged, adversaryResults)

	if o.config.Comments.MergeGateEnabled {
		scored = gates.ClassifyFindings(ctx, o.deps.App, scored)
	}
	return plan, scored, nil
}

// runCoverageAndConsistency runs the coverage loop and consistency verify
// concurrently (asyncio.gather(coverage_loop, consistency_verify)), preserving
// the ordered destructuring of the two results.
func (o *Orchestrator) runCoverageAndConsistency(
	ctx context.Context,
	plan schemas.ReviewPlan,
	anatomy schemas.AnatomyResult,
	allFindings []schemas.ReviewFinding,
	adversaryResults []schemas.AdversaryResult,
) ([]schemas.ReviewFinding, []schemas.AdversaryResult, []schemas.ReviewFinding, error) {
	var covFindings []schemas.ReviewFinding
	var covAdversary []schemas.AdversaryResult
	var cvFindings []schemas.ReviewFinding

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var e error
		covFindings, covAdversary, e = o.runCoverageLoop(gctx, plan, anatomy,
			append([]schemas.ReviewFinding(nil), allFindings...), adversaryResults)
		return e
	})
	g.Go(func() error {
		var e error
		cvFindings, e = o.runConsistencyVerify(gctx, append([]schemas.ReviewFinding(nil), allFindings...))
		return e
	})
	if err := g.Wait(); err != nil {
		return nil, nil, nil, err
	}
	return covFindings, covAdversary, cvFindings, nil
}

// postWorthinessSelect runs the pre-layer post-worthiness gate. Returns the kept
// subset and ok=false when the gate errored (Python's try/except → keep all).
func (o *Orchestrator) postWorthinessSelect(ctx context.Context, findings []schemas.ReviewFinding) ([]schemas.ReviewFinding, bool) {
	raw, err := o.rfns.postWorthiness(ctx, o.reasonerDeps(), reasoners.PostWorthinessInput{Findings: findings})
	if err != nil {
		return nil, false
	}
	// Default keep set = all indices (pw.get("keep_indices", range(len))).
	keep := make(map[int]struct{})
	if v, present := raw["keep_indices"]; present {
		for _, i := range getIntSlice(v) {
			keep[i] = struct{}{}
		}
	} else {
		for i := range findings {
			keep[i] = struct{}{}
		}
	}
	sel := make([]schemas.ReviewFinding, 0, len(findings))
	for i, f := range findings {
		if _, ok := keep[i]; ok {
			sel = append(sel, f)
		}
	}
	return sel, true
}

// ---- Phase 3: META-SELECTORS ----

func (o *Orchestrator) runMetaSelectors(
	ctx context.Context,
	intake schemas.IntakeResult,
	anatomy schemas.AnatomyResult,
	reviewDepth, reviewerFeedback string,
) (schemas.ReviewPlan, error) {
	if o.budgetOrTimeoutExhausted("meta_selectors") {
		return schemas.ReviewPlan{}, budgetExhaustedErr(o.budgetExhaustedMessage("meta-selectors"))
	}

	lensFns := map[string]func(context.Context, reasoners.Deps, reasoners.MetaInput) (map[string]any, error){
		"semantic":   o.rfns.metaSemantic,
		"mechanical": o.rfns.metaMechanical,
		"systemic":   o.rfns.metaSystemic,
	}

	// Order-preserving fan-out: pre-indexed results in enabledLenses order.
	type lensJob struct {
		lens string
		fn   func(context.Context, reasoners.Deps, reasoners.MetaInput) (map[string]any, error)
	}
	jobs := make([]lensJob, 0, len(enabledLenses))
	for _, lens := range enabledLenses {
		if fn, ok := lensFns[lens]; ok {
			jobs = append(jobs, lensJob{lens: lens, fn: fn})
		}
	}
	results := make([]schemas.MetaDimensionResult, len(jobs))
	diffPatches := reasoners.OrderedPatches(o.filePatches())

	g, gctx := errgroup.WithContext(ctx)
	for i := range jobs {
		i := i
		g.Go(func() error {
			raw, err := jobs[i].fn(gctx, o.reasonerDeps(), reasoners.MetaInput{
				Intake:           intake,
				Anatomy:          anatomy,
				Depth:            reviewDepth,
				RepoPath:         strp(o.input.RepoPath),
				DiffPatches:      diffPatches,
				ReviewerFeedback: reviewerFeedback,
			})
			if err != nil {
				return err
			}
			o.incInvocations(1)
			o.registerCost("meta_selectors", raw)
			results[i] = mapToStruct[schemas.MetaDimensionResult](raw)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return schemas.ReviewPlan{}, err
	}

	o.metaSelectorResults = results
	o.effectiveDepth = o.escalateDepth(reviewDepth)

	// Prefix each dimension id by its lens, preserving lens then dimension order.
	var allDimensions []schemas.ReviewDimension
	for _, meta := range results {
		for _, dim := range meta.Dimensions {
			d := dim
			d.ID = meta.Lens + "_" + dim.ID
			allDimensions = append(allDimensions, d)
		}
	}
	allDimensions = dedupCrossMeta(allDimensions)

	if profile, ok := config.DepthProfiles[reviewDepth]; ok && len(allDimensions) > profile.MaxDimensions {
		sort.SliceStable(allDimensions, func(i, j int) bool {
			return allDimensions[i].Priority > allDimensions[j].Priority
		})
		allDimensions = allDimensions[:profile.MaxDimensions]
	}

	// ReviewPlan is NOT default-seeded in schemas (design §5), so seed the
	// pydantic BudgetAllocation() default for total_budget explicitly — otherwise
	// plan.model_dump() (into metadata) would carry an all-zero budget.
	return schemas.ReviewPlan{
		Dimensions:    allDimensions,
		CrossRefHints: []string{},
		TotalBudget:   defaultBudgetAllocation(),
	}, nil
}

// defaultBudgetAllocation reproduces pydantic BudgetAllocation()'s seeded
// defaults (schemas defaults.go), for structs the orchestrator constructs
// directly (ReviewPlan.TotalBudget).
func defaultBudgetAllocation() schemas.BudgetAllocation {
	return schemas.BudgetAllocation{
		MaxCostUSD:          0.5,
		MaxDurationSeconds:  60,
		MaxReferenceFollows: 3,
		MaxChildSpawns:      2,
	}
}

// dedupCrossMeta ports _dedup_cross_meta: dedup by sorted target-files key,
// keeping the higher-priority dimension.
func dedupCrossMeta(dimensions []schemas.ReviewDimension) []schemas.ReviewDimension {
	seen := map[string]schemas.ReviewDimension{}
	var deduped []schemas.ReviewDimension
	for _, dim := range dimensions {
		key := append([]string(nil), dim.TargetFiles...)
		sort.Strings(key)
		keyStr := strings.Join(key, "|")
		if existing, ok := seen[keyStr]; ok {
			if dim.Priority > existing.Priority {
				filtered := deduped[:0]
				for _, d := range deduped {
					if d.ID != existing.ID {
						filtered = append(filtered, d)
					}
				}
				deduped = filtered
				deduped = append(deduped, dim)
				seen[keyStr] = dim
			}
		} else {
			seen[keyStr] = dim
			deduped = append(deduped, dim)
		}
	}
	return deduped
}

// ---- Phase 4: REVIEW (streaming producer) ----

// collectParallelReview drives runParallelReview to completion, closing the
// channel and collecting every batch (the gate-ON and coverage-gap pattern where
// Python awaits the producer fully then drains the queue).
func (o *Orchestrator) collectParallelReview(ctx context.Context, plan schemas.ReviewPlan, feedback string) ([]schemas.ReviewFinding, error) {
	ch := make(chan []schemas.ReviewFinding)
	errc := make(chan error, 1)
	stats := &dimensionParseStats{}
	go func() {
		err := o.runParallelReview(ctx, plan, ch, 0, feedback, stats)
		close(ch)
		errc <- err
	}()
	var out []schemas.ReviewFinding
	for batch := range ch {
		out = append(out, batch...)
	}
	if err := <-errc; err != nil {
		return nil, err
	}
	if err := allDimensionsSchemaFailed(stats.snapshot()); err != nil {
		return nil, err
	}
	return out, nil
}

func allDimensionsSchemaFailed(stats dimensionParseSnapshot) error {
	if stats.Attempted > 0 && stats.Parseable == 0 && stats.Failed > 0 {
		return fmt.Errorf("all review dimensions failed schema parsing (failed dimensions: %d)", stats.Failed)
	}
	return nil
}

// runParallelReview ports _run_parallel_review. It fans out one reviewer per
// dimension bounded by a weighted semaphore, streaming each reviewer's findings
// to ch as it completes; a reviewer may recursively spawn ≤2 sub-reviews up to
// max_review_depth. It does NOT close ch — the caller owns the sentinel/close.
func (o *Orchestrator) runParallelReview(
	ctx context.Context,
	plan schemas.ReviewPlan,
	ch chan<- []schemas.ReviewFinding,
	currentDepth int,
	feedback string,
	stats *dimensionParseStats,
) error {
	maxDepth := o.config.Budget.MaxReviewDepth
	sem := semaphore.NewWeighted(int64(o.config.Budget.MaxConcurrentReviewers))
	g, gctx := errgroup.WithContext(ctx)
	prDescription := ""
	if o.prData != nil {
		prDescription = truncateRunes(strings.TrimSpace(o.prData.Description), 4000)
	}

	var runDim func(dim schemas.ReviewDimension, depth int)
	runDim = func(dim schemas.ReviewDimension, depth int) {
		g.Go(func() error {
			if o.budgetOrTimeoutExhausted("review") {
				return nil
			}
			if err := sem.Acquire(gctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)

			// Filter patches to this dimension's target files (order immaterial).
			targets := stringSet(dim.TargetFiles)
			dimPatches := map[string]string{}
			for _, p := range o.filePatches() {
				if _, ok := targets[p.Key]; ok {
					dimPatches[p.Key] = p.Val
				}
			}

			primed := ""
			if o.config.Budget.EvidencePackReviewers && strp(o.input.RepoPath) != "" {
				primed = evidence.BuildDimensionPack(gctx, strp(o.input.RepoPath), dim.TargetFiles, dimPatches)
			}

			var narrative, intakeSummary string
			var riskSurfaces []string
			if o.anatomyResult != nil {
				narrative = o.anatomyResult.PrNarrative
				riskSurfaces = o.anatomyResult.RiskSurfaces
			}
			if o.intakeResult != nil {
				intakeSummary = o.intakeResult.PrSummary
			}
			otherNames := make([]string, 0, len(plan.Dimensions))
			for _, d := range plan.Dimensions {
				if d.ID != dim.ID {
					otherNames = append(otherNames, d.Name)
				}
			}
			var patchArg map[string]string
			if len(dimPatches) > 0 {
				patchArg = dimPatches
			}

			stats.recordAttempt()
			o.recordDimensionAttempt()
			resultRaw, err := o.rfns.reviewDim(gctx, o.reasonerDeps(), reasoners.ReviewDimensionInput{
				ReviewPrompt:      dim.ReviewPrompt,
				TargetFiles:       dim.TargetFiles,
				ContextFiles:      dim.ContextFiles,
				RepoPath:          strp(o.input.RepoPath),
				CurrentDepth:      depth,
				MaxDepth:          maxDepth,
				PrNarrative:       narrative,
				RiskSurfaces:      riskSurfaces,
				IntakeSummary:     intakeSummary,
				PrDescription:     prDescription,
				DiffPatches:       patchArg,
				AllDimensionNames: otherNames,
				ReviewerFeedback:  feedback,
				PrimedCode:        primed,
			})
			if err != nil {
				return err
			}
			o.incInvocations(1)
			o.registerCost("review", resultRaw)
			schemaParseFailed := getBoolDefault(unwrap(resultRaw), "schema_parse_failed", false)
			stats.recordResult(schemaParseFailed)
			o.recordDimensionResult(schemaParseFailed)
			if schemaParseFailed {
				select {
				case ch <- []schemas.ReviewFinding{}:
				case <-gctx.Done():
					return gctx.Err()
				}
				return nil
			}

			findings := o.extractFindings(resultRaw, dim)
			select {
			case ch <- findings:
			case <-gctx.Done():
				return gctx.Err()
			}

			subReviews := extractSubReviews(resultRaw, dim)
			if len(subReviews) > 0 && depth < maxDepth && !o.budgetOrTimeoutExhausted("review") {
				for _, sub := range subReviews {
					runDim(sub, depth+1)
				}
			}
			return nil
		})
	}

	for _, dim := range plan.Dimensions {
		runDim(dim, currentDepth)
	}
	return g.Wait()
}

// extractSubReviews ports _extract_sub_reviews: at most the first 2 sub_reviews
// with a non-empty prompt and target_files become child dimensions.
func extractSubReviews(resultRaw map[string]any, parentDim schemas.ReviewDimension) []schemas.ReviewDimension {
	payload := unwrap(resultRaw)
	if payload == nil {
		return nil
	}
	rawSubs, ok := payload["sub_reviews"].([]any)
	if !ok {
		return nil
	}
	var dims []schemas.ReviewDimension
	for idx, s := range rawSubs {
		if idx >= 2 {
			break
		}
		sub, ok := s.(map[string]any)
		if !ok {
			continue
		}
		prompt := getStr(sub, "review_prompt", "")
		targets := getStrSlice(sub, "target_files")
		if prompt == "" || len(targets) == 0 {
			continue
		}
		reason := getStr(sub, "reason", "deep-dive")
		dims = append(dims, schemas.ReviewDimension{
			ID:           parentDim.ID + "_sub" + itoa(idx),
			Name:         parentDim.Name + " → " + truncateRunes(reason, 40),
			ReviewPrompt: prompt,
			TargetFiles:  targets,
			ContextFiles: getStrSlice(sub, "context_files"),
			Priority:     parentDim.Priority,
		})
	}
	return dims
}

// extractFindings ports _extract_findings.
func (o *Orchestrator) extractFindings(resultRaw map[string]any, dim schemas.ReviewDimension) []schemas.ReviewFinding {
	payload := unwrap(resultRaw)
	var raw []map[string]any
	if payload != nil {
		if lst := asObjListStrict(payload, "findings"); lst != nil {
			raw = lst
		} else if lst := asObjListStrict(payload, "results"); lst != nil {
			raw = lst
		}
	}
	findings := make([]schemas.ReviewFinding, 0, len(raw))
	for _, item := range raw {
		findings = append(findings, mapToReviewFinding(item, map[string]any{
			"dimension_id":   dim.ID,
			"dimension_name": dim.Name,
			"title":          "Untitled finding",
		}))
	}
	return findings
}

// streamReviewLayer runs the gate-OFF default path: _run_parallel_review and
// _run_review_layer CONCURRENTLY, findings streaming through one channel
// (producer errgroup closes on completion; the layer ranges it). This is NOT a
// gather-then-process — the layer begins consuming as reviewers complete
// (design risk 1). A producer error still closes the channel so the consumer's
// range terminates; the errgroup surfaces the first error.
func (o *Orchestrator) streamReviewLayer(
	ctx context.Context,
	plan schemas.ReviewPlan,
	anatomy schemas.AnatomyResult,
	reviewerFeedback string,
) ([]schemas.ReviewFinding, []schemas.AdversaryResult, error) {
	ch := make(chan []schemas.ReviewFinding)
	var allFindings []schemas.ReviewFinding
	var adversaryResults []schemas.AdversaryResult
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(ch)
		stats := &dimensionParseStats{}
		if err := o.runParallelReview(gctx, plan, ch, 0, reviewerFeedback, stats); err != nil {
			return err
		}
		return allDimensionsSchemaFailed(stats.snapshot())
	})
	g.Go(func() error {
		var e error
		allFindings, adversaryResults, e = o.runReviewLayer(gctx, plan, ch, anatomy)
		return e
	})
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return allFindings, adversaryResults, nil
}

// ---- Phase 5: LAYER (streaming consumer) ----

func (o *Orchestrator) runReviewLayer(
	ctx context.Context,
	plan schemas.ReviewPlan,
	ch <-chan []schemas.ReviewFinding,
	anatomy schemas.AnatomyResult,
) ([]schemas.ReviewFinding, []schemas.AdversaryResult, error) {
	_ = plan
	var allFindings []schemas.ReviewFinding
	for batch := range ch {
		if o.layerBatchHook != nil {
			o.layerBatchHook(batch)
		}
		allFindings = append(allFindings, batch...)
	}

	evidenceMap := map[string]evidence.EvidencePackage{}
	if len(allFindings) > 0 && strp(o.input.RepoPath) != "" {
		em, err := evidence.ExtractEvidenceForFindings(ctx, allFindings, strp(o.input.RepoPath), o.filePatchesMap(), o.blastRadius())
		if err != nil {
			return nil, nil, err
		}
		evidenceMap = em
	}

	verificationMap := map[string]map[string]any{}
	if hasHighPriority(allFindings) && len(evidenceMap) > 0 && !o.budgetOrTimeoutExhausted("adversary") {
		updated, vmap, err := o.runEvidenceVerification(ctx, allFindings, evidenceMap)
		if err != nil {
			return nil, nil, err
		}
		allFindings = updated
		verificationMap = vmap
	}

	var adversaryResults []schemas.AdversaryResult
	if len(allFindings) > 0 && !o.budgetOrTimeoutExhausted("adversary") {
		ar, err := o.runParallelAdversary(ctx, allFindings, evidenceMap, verificationMap)
		if err != nil {
			return nil, nil, err
		}
		adversaryResults = ar
	}

	challenged := challengedTitles(adversaryResults)
	var confirmed []schemas.ReviewFinding
	for _, f := range allFindings {
		if _, ok := challenged[f.Title]; !ok {
			confirmed = append(confirmed, f)
		}
	}

	compound, err := o.runCompoundAnalysis(ctx, confirmed, evidenceMap)
	if err != nil {
		return nil, nil, err
	}
	allFindings = append(allFindings, compound...)
	return allFindings, adversaryResults, nil
}

// runEvidenceVerification ports _run_evidence_verification.
func (o *Orchestrator) runEvidenceVerification(
	ctx context.Context,
	findings []schemas.ReviewFinding,
	evidenceMap map[string]evidence.EvidencePackage,
) ([]schemas.ReviewFinding, map[string]map[string]any, error) {
	var highPriority []schemas.ReviewFinding
	for _, f := range findings {
		if f.Severity == "critical" || f.Severity == "important" {
			highPriority = append(highPriority, f)
		}
	}
	if len(highPriority) == 0 {
		return findings, map[string]map[string]any{}, nil
	}

	evPackages := map[string]map[string]any{}
	for _, f := range highPriority {
		if pkg, ok := evidenceMap[f.Title]; ok {
			evPackages[f.Title] = evidencePackToMap(pkg)
		}
	}
	var evArg map[string]map[string]any
	if len(evPackages) > 0 {
		evArg = evPackages
	}

	raw, err := o.rfns.evidenceVerify(ctx, o.reasonerDeps(), reasoners.EvidenceVerifierInput{
		Findings:         highPriority,
		EvidencePackages: evArg,
		PrContext:        o.buildPRContextString(),
		RepoPath:         strp(o.input.RepoPath),
	})
	if err != nil {
		return nil, nil, err
	}
	o.incInvocations(1)
	o.registerCost("adversary", raw)

	verificationMap := map[string]map[string]any{}
	for _, vf := range asObjListStrict(raw, "verified_findings") {
		title := getStr(vf, "title", "")
		if title == "" {
			continue
		}
		verificationMap[title] = vf
	}

	updated := make([]schemas.ReviewFinding, 0, len(findings))
	for _, f := range findings {
		vf, ok := verificationMap[f.Title]
		if !ok {
			updated = append(updated, f)
			continue
		}
		verified := getBoolDefault(vf, "verified", true)
		if !verified {
			nf := f
			conf := getFloatOr(vf, "revised_confidence", 0.3)
			if conf < 0.1 {
				conf = 0.1
			}
			nf.Confidence = conf
			nf.Severity = schemas.NormalizeSeverity(vf["revised_severity"], schemas.DefaultSeverity)
			updated = append(updated, nf)
			continue
		}
		nf := f
		changed := false
		if rc, present := vf["revised_confidence"]; present {
			if c, ok := asFloat(rc); ok {
				nf.Confidence = c
				changed = true
			}
		}
		if rs, present := vf["revised_severity"]; present {
			if s, ok := rs.(string); ok && s != "" {
				nf.Severity = schemas.NormalizeSeverity(s, schemas.DefaultSeverity)
				changed = true
			}
		}
		_ = changed
		updated = append(updated, nf)
	}
	return updated, verificationMap, nil
}

// runParallelAdversary ports _run_parallel_adversary (order-preserving batches).
func (o *Orchestrator) runParallelAdversary(
	ctx context.Context,
	findings []schemas.ReviewFinding,
	evidenceMap map[string]evidence.EvidencePackage,
	verificationMap map[string]map[string]any,
) ([]schemas.AdversaryResult, error) {
	if len(findings) == 0 || o.budgetOrTimeoutExhausted("adversary") {
		return nil, nil
	}
	aiConfidence := 0.0
	if o.intakeResult != nil {
		aiConfidence = o.intakeResult.AIGenerated
	}

	var batches [][]schemas.ReviewFinding
	for i := 0; i < len(findings); i += adversaryBatchSize {
		end := i + adversaryBatchSize
		if end > len(findings) {
			end = len(findings)
		}
		batches = append(batches, findings[i:end])
		if len(batches) >= maxAdversaryBatch {
			break
		}
	}

	results := make([][]schemas.AdversaryResult, len(batches))
	g, gctx := errgroup.WithContext(ctx)
	for i := range batches {
		i := i
		g.Go(func() error {
			if o.budgetOrTimeoutExhausted("adversary") {
				return nil
			}
			batch := batches[i]
			batchEvidence := map[string]map[string]any{}
			for _, f := range batch {
				entry := map[string]any{}
				if pkg, ok := evidenceMap[f.Title]; ok {
					entry = evidencePackToMap(pkg)
				}
				if vf, ok := verificationMap[f.Title]; ok {
					entry["verification"] = map[string]any{
						"verified":           mapGet(vf, "verified", true),
						"actual_behavior":    getStr(vf, "actual_behavior", ""),
						"verification_notes": getStr(vf, "verification_notes", ""),
					}
				}
				if len(entry) > 0 {
					batchEvidence[f.Title] = entry
				}
			}
			var evArg map[string]map[string]any
			if len(batchEvidence) > 0 {
				evArg = batchEvidence
			}
			raw, err := o.rfns.adversary(gctx, o.reasonerDeps(), reasoners.AdversaryInput{
				Findings:              batch,
				AIGeneratedConfidence: aiConfidence,
				PrContext:             o.buildPRContextString(),
				RepoPath:              strp(o.input.RepoPath),
				EvidencePackages:      evArg,
			})
			if err != nil {
				return err
			}
			o.incInvocations(1)
			o.registerCost("adversary", raw)
			results[i] = extractAdversaryResults(raw)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var all []schemas.AdversaryResult
	for _, br := range results {
		all = append(all, br...)
	}
	return all, nil
}

// extractAdversaryResults ports _extract_adversary_results.
func extractAdversaryResults(resultRaw map[string]any) []schemas.AdversaryResult {
	payload := unwrap(resultRaw)
	var raw []map[string]any
	if payload != nil {
		for _, key := range []string{"results", "adversary_results", "findings"} {
			if lst := asObjListStrict(payload, key); lst != nil {
				raw = lst
				break
			}
		}
	}
	out := make([]schemas.AdversaryResult, 0, len(raw))
	for _, item := range raw {
		out = append(out, mapToStruct[schemas.AdversaryResult](item))
	}
	return out
}

// ---- Phase 6: COVERAGE LOOP ----

func (o *Orchestrator) runCoverageLoop(
	ctx context.Context,
	plan schemas.ReviewPlan,
	anatomy schemas.AnatomyResult,
	findings []schemas.ReviewFinding,
	adversaryResults []schemas.AdversaryResult,
) ([]schemas.ReviewFinding, []schemas.AdversaryResult, error) {
	for iter := 0; iter < o.config.Budget.MaxCoverageIterations; iter++ {
		if o.budgetOrTimeoutExhausted("coverage") {
			break
		}
		reviewedClusters := reviewedClusters(anatomy, findings)
		dimensionNames := make([]string, 0, len(plan.Dimensions))
		for _, d := range plan.Dimensions {
			dimensionNames = append(dimensionNames, d.Name)
		}
		gateRaw, err := o.rfns.coverageGate(ctx, o.reasonerDeps(), reasoners.CoverageGateInput{
			Anatomy:                anatomy,
			ReviewedClusters:       reviewedClusters,
			DimensionNamesReviewed: dimensionNames,
		})
		if err != nil {
			return nil, nil, err
		}
		o.incInvocations(1)
		o.registerCost("coverage", gateRaw)

		fullyCovered := getBoolDefault(gateRaw, "fully_covered", false)
		confident := getBoolDefault(gateRaw, "confident", true)
		gapDescriptions := getStrSlice(gateRaw, "gap_descriptions")
		o.coverageIterations++

		if fullyCovered || !confident || len(gapDescriptions) == 0 {
			break
		}
		gapDims := buildGapDimensions(anatomy, gapDescriptions, reviewedClusters)
		if len(gapDims) == 0 {
			break
		}

		gapFindings, err := o.collectParallelReview(ctx, schemas.ReviewPlan{
			Dimensions:    gapDims,
			CrossRefHints: plan.CrossRefHints,
		}, "")
		if err != nil {
			return nil, nil, err
		}
		findings = append(findings, gapFindings...)

		gapEvidence := map[string]evidence.EvidencePackage{}
		if len(findings) > 0 && strp(o.input.RepoPath) != "" {
			em, err := evidence.ExtractEvidenceForFindings(ctx, findings, strp(o.input.RepoPath), o.filePatchesMap(), o.blastRadius())
			if err != nil {
				return nil, nil, err
			}
			gapEvidence = em
		}
		if len(findings) > 0 && !o.budgetOrTimeoutExhausted("adversary") {
			ar, err := o.runParallelAdversary(ctx, findings, gapEvidence, map[string]map[string]any{})
			if err != nil {
				return nil, nil, err
			}
			adversaryResults = ar
		}
		challenged := challengedTitles(adversaryResults)
		var kept []schemas.ReviewFinding
		for _, f := range findings {
			if _, ok := challenged[f.Title]; !ok {
				kept = append(kept, f)
			}
		}
		findings = kept
	}
	return findings, adversaryResults, nil
}

// reviewedClusters ports _reviewed_clusters.
func reviewedClusters(anatomy schemas.AnatomyResult, findings []schemas.ReviewFinding) []string {
	findingPaths := map[string]struct{}{}
	for _, f := range findings {
		if f.FilePath != "" {
			findingPaths[f.FilePath] = struct{}{}
		}
	}
	reviewed := map[string]struct{}{}
	for _, cluster := range anatomy.Clusters {
		for _, path := range cluster.Files {
			if _, ok := findingPaths[path]; ok {
				reviewed[cluster.ID] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(reviewed))
	for id := range reviewed {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// buildGapDimensions ports _build_gap_dimensions.
func buildGapDimensions(anatomy schemas.AnatomyResult, gapDescriptions, reviewedClusters []string) []schemas.ReviewDimension {
	reviewed := stringSet(reviewedClusters)
	var candidates []schemas.ChangeCluster
	for _, c := range anatomy.Clusters {
		if _, ok := reviewed[c.ID]; !ok {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	var dims []schemas.ReviewDimension
	for idx, gap := range gapDescriptions {
		if idx >= len(candidates) {
			break
		}
		cluster := candidates[idx]
		dims = append(dims, schemas.ReviewDimension{
			ID:           "coverage_gap_" + itoa(idx),
			Name:         "Coverage Gap " + itoa(idx+1),
			ReviewPrompt: prompts.CoverageGapPrompt(gap),
			TargetFiles:  cluster.Files,
			ContextFiles: []string{},
			Priority:     1,
		})
	}
	return dims
}

// ---- Phase 6.7: CONSISTENCY VERIFY ----

func (o *Orchestrator) runConsistencyVerify(ctx context.Context, allFindings []schemas.ReviewFinding) ([]schemas.ReviewFinding, error) {
	if o.budgetOrTimeoutExhausted("review") {
		return allFindings, nil
	}
	diffPatches := reasoners.OrderedPatches(o.filePatches())
	if len(diffPatches) == 0 {
		return allFindings, nil
	}
	repo := strp(o.input.RepoPath)

	obRaw, err := o.rfns.extractOblig(ctx, o.reasonerDeps(), reasoners.ExtractObligationsInput{
		DiffPatches: diffPatches,
		RepoPath:    repo,
		PrContext:   o.buildPRContextString(),
	})
	if err != nil {
		// Python: except → skip, return existing findings unchanged.
		return allFindings, nil
	}
	o.incInvocations(1)
	o.registerCost("review", obRaw)

	obligations := asObjListStrict(obRaw, "obligations")
	if len(obligations) > 12 {
		obligations = obligations[:12]
	}
	if len(obligations) == 0 {
		return allFindings, nil
	}

	// Order-preserving fan-out: one verify_obligation per obligation.
	verdicts := make([]map[string]any, len(obligations))
	var wg sync.WaitGroup
	for i := range obligations {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := o.rfns.verifyOblig(ctx, o.reasonerDeps(), reasoners.VerifyObligationInput{
				Obligation: obligations[i],
				RepoPath:   repo,
			})
			if err != nil {
				verdicts[i] = map[string]any{"holds": true}
				return
			}
			verdicts[i] = raw
		}()
	}
	wg.Wait()
	o.incInvocations(len(verdicts))

	var newFindings []schemas.ReviewFinding
	for _, v := range verdicts {
		if v == nil {
			continue
		}
		holds, present := v["holds"]
		if !present || holds != false || getStr(v, "title", "") == "" {
			continue
		}
		lineEndVal := getIntOrZero(v, "line_end")
		if lineEndVal == 0 {
			lineEndVal = getIntOrZero(v, "line_start")
		}
		normalized := map[string]any{
			"dimension_id":   "consistency-verify",
			"dimension_name": "Consistency Verifier",
			"file_path":      getStr(v, "file_path", ""),
			"line_start":     getIntOrZero(v, "line_start"),
			"line_end":       lineEndVal,
			"severity":       severityOr(v, "important"),
			"title":          getStr(v, "title", ""),
			"body":           getStr(v, "body", ""),
			"suggestion":     v["suggestion"],
			"evidence":       getStr(v, "evidence", ""),
			"confidence":     getFloatOr(v, "confidence", 0.7),
			"tags":           []string{"consistency"},
		}
		newFindings = append(newFindings, mapToStruct[schemas.ReviewFinding](normalized))
	}
	return append(allFindings, newFindings...), nil
}

// ---- shared phase helpers ----

func (o *Orchestrator) filePatches() []prompts.StrPair {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.patchesCacheSet {
		return o.patchesCache
	}
	var pairs []prompts.StrPair
	if o.prData != nil {
		idx := map[string]int{}
		for _, cf := range o.prData.ChangedFiles {
			if cf.Patch == "" {
				continue
			}
			if i, ok := idx[cf.Path]; ok {
				pairs[i].Val = cf.Patch
				continue
			}
			idx[cf.Path] = len(pairs)
			pairs = append(pairs, prompts.StrPair{Key: cf.Path, Val: cf.Patch})
		}
	}
	o.patchesCache = pairs
	o.patchesCacheSet = true
	return pairs
}

func (o *Orchestrator) filePatchesMap() map[string]string {
	pairs := o.filePatches()
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		m[p.Key] = p.Val
	}
	return m
}

func (o *Orchestrator) blastRadius() []string {
	if o.anatomyResult != nil {
		return o.anatomyResult.BlastRadius
	}
	return nil
}

// buildPRContextString ports _build_pr_context_string.
func (o *Orchestrator) buildPRContextString() string {
	var parts []string
	if o.intakeResult != nil {
		parts = append(parts, "PR Type: "+o.intakeResult.PrType)
		parts = append(parts, "Complexity: "+o.intakeResult.Complexity)
		parts = append(parts, "Summary: "+o.intakeResult.PrSummary)
		if len(o.intakeResult.RiskSignals) > 0 {
			parts = append(parts, "Risk Signals: "+strings.Join(o.intakeResult.RiskSignals, ", "))
		}
	}
	if o.anatomyResult != nil {
		parts = append(parts, "PR Narrative: "+o.anatomyResult.PrNarrative)
		if len(o.anatomyResult.IntentGaps) > 0 {
			parts = append(parts, "Intent Gaps: "+strings.Join(o.anatomyResult.IntentGaps, ", "))
		}
	}
	return strings.Join(parts, "\n")
}

// cleanupContextDir ports _cleanup_context_dir.
func (o *Orchestrator) cleanupContextDir() {
	repoPath := strp(o.input.RepoPath)
	if repoPath == "" {
		return
	}
	ctxDir := filepath.Join(repoPath, ".pr-af-context")
	if info, err := os.Stat(ctxDir); err == nil && info.IsDir() {
		_ = os.RemoveAll(ctxDir)
	}
}

func toChangedFiles(files []schemas.FileChange) []schemas.ChangedFile {
	out := make([]schemas.ChangedFile, 0, len(files))
	for _, fc := range files {
		hunks := make([]string, 0, len(fc.Hunks))
		for _, h := range fc.Hunks {
			hunks = append(hunks, h.Content)
		}
		out = append(out, schemas.ChangedFile{
			Path:      fc.Path,
			Status:    fc.Status,
			Additions: fc.LinesAdded,
			Deletions: fc.LinesRemoved,
			Patch:     strings.Join(hunks, "\n\n"),
		})
	}
	return out
}

func hasHighPriority(findings []schemas.ReviewFinding) bool {
	for _, f := range findings {
		if f.Severity == "critical" || f.Severity == "important" {
			return true
		}
	}
	return false
}

func challengedTitles(results []schemas.AdversaryResult) map[string]struct{} {
	out := map[string]struct{}{}
	for _, ar := range results {
		if ar.Verdict == "challenged" {
			out[ar.FindingTitle] = struct{}{}
		}
	}
	return out
}

func evidencePackToMap(pkg evidence.EvidencePackage) map[string]any {
	m, _ := structToMap(&pkg)
	return m
}
