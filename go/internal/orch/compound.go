package orch

// compound.go ports the Phase 5.5 compound (cross-reference) analysis from
// orchestrator.py: _run_compound_analysis, _dedup_compound_findings,
// _select_compound_clusters, _extract_compound_findings.
//
// Determinism note: Python's _select_compound_clusters derives some candidate
// groups from Python `set` iteration (import tokens, caller-snippet keys), whose
// order is non-deterministic per run — so the `order` counter (and thus the
// tie-break among equal-priority import/caller candidates) is already
// non-deterministic in Python. The Go port iterates those set-derived groups in
// sorted order for a STABLE result; file/tag/dir groups preserve finding order
// exactly as Python does.

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/Agent-Field/pr-af/go/internal/evidence"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// runCompoundAnalysis ports _run_compound_analysis with order-preserving
// pre-indexed results. A failed cluster now fails the required cross-reference
// phase instead of silently erasing a possible compound finding.
func (o *Orchestrator) runCompoundAnalysis(
	ctx context.Context,
	confirmedFindings []schemas.ReviewFinding,
	evidenceMap map[string]evidence.EvidencePackage,
) ([]schemas.ReviewFinding, error) {
	clusters := selectCompoundClusters(confirmedFindings, evidenceMap, o.config.Budget.MaxCrossRefDeepDives)
	if len(clusters) == 0 {
		return nil, nil
	}
	if o.budgetOrTimeoutExhausted("cross_ref") {
		return nil, budgetExhaustedErr(o.budgetExhaustedMessage("cross-reference analysis"))
	}

	results := make([]map[string]any, len(clusters))
	g, gctx := errgroup.WithContext(ctx)
	for i := range clusters {
		i := i
		g.Go(func() error {
			cluster := clusters[i]
			clusterEvidence := map[string]map[string]any{}
			if len(evidenceMap) > 0 {
				for _, f := range cluster {
					if pkg, ok := evidenceMap[f.Title]; ok {
						clusterEvidence[f.Title] = evidencePackToMap(pkg)
					}
				}
			}
			var evArg map[string]map[string]any
			if len(clusterEvidence) > 0 {
				evArg = clusterEvidence
			}
			raw, err := o.rfns.compoundFinder(gctx, o.reasonerDeps(), reasoners.CompoundFinderInput{
				ClusterFindings: cluster,
				RepoPath:        strp(o.input.RepoPath),
				EvidenceMap:     evArg,
			})
			if err != nil {
				return err
			}
			results[i] = raw
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var compoundFindings []schemas.ReviewFinding
	for _, raw := range results {
		o.incInvocations(1)
		o.registerCost("cross_ref", raw)
		compoundFindings = append(compoundFindings, extractCompoundFindings(raw)...)
	}

	if len(compoundFindings) > 1 {
		deduped, err := o.dedupCompoundFindings(ctx, compoundFindings, confirmedFindings)
		if err != nil {
			return nil, err
		}
		compoundFindings = deduped
	}

	o.mu.Lock()
	o.crossRefCount += len(compoundFindings)
	o.mu.Unlock()
	return compoundFindings, nil
}

// dedupCompoundFindings ports _dedup_compound_findings.
func (o *Orchestrator) dedupCompoundFindings(
	ctx context.Context,
	compoundFindings, individualFindings []schemas.ReviewFinding,
) ([]schemas.ReviewFinding, error) {
	limit := len(individualFindings)
	if limit > 20 {
		limit = 20
	}
	summaryLines := make([]string, 0, limit)
	for _, f := range individualFindings[:limit] {
		summaryLines = append(summaryLines, "- ["+string(f.Severity)+"] "+f.Title+" ("+f.FilePath+")")
	}
	individualSummary := strings.Join(summaryLines, "\n")

	raw, err := o.rfns.compoundDedup(ctx, o.reasonerDeps(), reasoners.CompoundDedupInput{
		CompoundFindings:          compoundFindings,
		IndividualFindingsSummary: individualSummary,
	})
	if err != nil {
		return nil, err
	}
	o.incInvocations(1)
	o.registerCost("cross_ref", raw)

	payload := unwrap(raw)
	var keepIndices []int
	if payload != nil {
		keepIndices = getIntSlice(payload["keep_indices"])
	}
	if len(keepIndices) == 0 {
		return compoundFindings, nil
	}
	var deduped []schemas.ReviewFinding
	for _, i := range keepIndices {
		if i >= 0 && i < len(compoundFindings) {
			deduped = append(deduped, compoundFindings[i])
		}
	}
	if len(deduped) == 0 {
		return compoundFindings, nil
	}
	return deduped, nil
}

// extractCompoundFindings ports _extract_compound_findings.
func extractCompoundFindings(resultRaw map[string]any) []schemas.ReviewFinding {
	payload := unwrap(resultRaw)
	var raw []map[string]any
	if payload != nil {
		if lst := asObjListStrict(payload, "findings"); lst != nil {
			raw = lst
		} else if lst := asObjListStrict(payload, "results"); lst != nil {
			raw = lst
		}
	}
	out := make([]schemas.ReviewFinding, 0, len(raw))
	for _, item := range raw {
		out = append(out, mapToReviewFinding(item, map[string]any{
			"dimension_id":      "compound",
			"dimension_name":    "Compound Analysis",
			"title":             "Untitled compound finding",
			"severity":          "important",
			"line_end_fallback": "line_start",
		}))
	}
	return out
}

var importTokenRe = regexp.MustCompile(`[a-z0-9_./]+`)

// selectCompoundClusters ports _select_compound_clusters.
func selectCompoundClusters(
	findings []schemas.ReviewFinding,
	evidenceMap map[string]evidence.EvidencePackage,
	maxClusters int,
) [][]schemas.ReviewFinding {
	if len(findings) < 2 {
		return nil
	}

	byTitle := map[string]schemas.ReviewFinding{}
	var titleOrder []string
	for _, f := range findings {
		if _, ok := byTitle[f.Title]; !ok {
			titleOrder = append(titleOrder, f.Title)
		}
		byTitle[f.Title] = f
	}

	type candidate struct {
		priority int
		order    int
		cluster  []schemas.ReviewFinding
	}
	var candidates []candidate
	seenSignatures := map[string]struct{}{}
	order := 0

	addCandidate := func(priority int, cluster []schemas.ReviewFinding) {
		// Dedup by title (last value wins, first position kept).
		uniq := map[string]schemas.ReviewFinding{}
		var uniqOrder []string
		for _, f := range cluster {
			if _, ok := uniq[f.Title]; !ok {
				uniqOrder = append(uniqOrder, f.Title)
			}
			uniq[f.Title] = f
		}
		normalized := make([]schemas.ReviewFinding, 0, len(uniqOrder))
		for _, t := range uniqOrder {
			normalized = append(normalized, uniq[t])
		}
		if len(normalized) < 2 {
			return
		}
		sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Title < normalized[j].Title })
		if len(normalized) > 4 {
			normalized = normalized[:4]
		}
		sig := make([]string, len(normalized))
		for i, f := range normalized {
			sig[i] = f.Title
		}
		sort.Strings(sig)
		sigStr := strings.Join(sig, "\x00")
		if _, ok := seenSignatures[sigStr]; ok {
			return
		}
		seenSignatures[sigStr] = struct{}{}
		candidates = append(candidates, candidate{priority: priority, order: order, cluster: normalized})
		order++
	}

	// file_groups (priority 0) — grouped by file_path, first-seen order.
	fileGroups := newOrderedGroups()
	for _, f := range findings {
		if f.FilePath != "" {
			fileGroups.add(f.FilePath, f)
		}
	}
	for _, key := range fileGroups.keys {
		if g := fileGroups.m[key]; len(g) >= 2 {
			addCandidate(0, g)
		}
	}

	// import_groups (priority 1) — token → titles; sorted-token order (stable).
	importTokens := func(title string) []string {
		pkg, ok := evidenceMap[title]
		if !ok {
			return nil
		}
		text := strings.ToLower(pkg.ImportContext)
		set := map[string]struct{}{}
		for _, tok := range importTokenRe.FindAllString(text, -1) {
			if len(tok) > 2 {
				set[tok] = struct{}{}
			}
		}
		out := make([]string, 0, len(set))
		for t := range set {
			out = append(out, t)
		}
		sort.Strings(out)
		return out
	}
	importGroups := map[string]map[string]struct{}{}
	for _, title := range titleOrder {
		for _, token := range importTokens(title) {
			if importGroups[token] == nil {
				importGroups[token] = map[string]struct{}{}
			}
			importGroups[token][title] = struct{}{}
		}
	}
	for _, token := range sortedKeys(importGroups) {
		titles := importGroups[token]
		if len(titles) >= 2 {
			addCandidate(1, findingsForTitles(sortedSet(titles), byTitle))
		}
	}

	// caller_groups (priority 3) — snippet key → titles; sorted-key order.
	callerGroups := map[string]map[string]struct{}{}
	for _, title := range titleOrder {
		pkg, ok := evidenceMap[title]
		if !ok {
			continue
		}
		f := byTitle[title]
		limit := len(pkg.CallerSnippets)
		if limit > 8 {
			limit = 8
		}
		for _, snippet := range pkg.CallerSnippets[:limit] {
			key := strings.ToLower(strings.TrimSpace(snippet))
			key = truncateRunes(key, 180)
			if key == "" {
				continue
			}
			if callerGroups[key] == nil {
				callerGroups[key] = map[string]struct{}{}
			}
			callerGroups[key][f.Title] = struct{}{}
		}
	}
	for _, key := range sortedKeys(callerGroups) {
		titles := callerGroups[key]
		if len(titles) >= 2 {
			addCandidate(3, findingsForTitles(sortedSet(titles), byTitle))
		}
	}

	// tag_groups (priority 2) — key tags, first-seen finding order.
	keyTags := map[string]struct{}{"security": {}, "auth": {}, "validation": {}, "error-handling": {}}
	tagGroups := newOrderedGroups()
	for _, f := range findings {
		for _, tag := range f.Tags {
			lowered := strings.ToLower(tag)
			if _, ok := keyTags[lowered]; ok {
				tagGroups.add(lowered, f)
			}
		}
	}
	for _, key := range tagGroups.keys {
		if g := tagGroups.m[key]; len(g) >= 2 {
			addCandidate(2, g)
		}
	}

	// dir_groups (priority 4) — dirname, first-seen order.
	dirGroups := newOrderedGroups()
	for _, f := range findings {
		if f.FilePath != "" {
			if dir := pyDirname(f.FilePath); dir != "" {
				dirGroups.add(dir, f)
			}
		}
	}
	for _, key := range dirGroups.keys {
		if g := dirGroups.m[key]; len(g) >= 2 {
			addCandidate(4, g)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].order < candidates[j].order
	})

	out := make([][]schemas.ReviewFinding, 0, maxClusters)
	for i, c := range candidates {
		if i >= maxClusters {
			break
		}
		out = append(out, c.cluster)
	}
	return out
}

// orderedGroups is an insertion-ordered multimap (Python dict.setdefault(...)
// .append(...) over .values() in insertion order).
type orderedGroups struct {
	keys []string
	m    map[string][]schemas.ReviewFinding
}

func newOrderedGroups() *orderedGroups {
	return &orderedGroups{m: map[string][]schemas.ReviewFinding{}}
}

func (g *orderedGroups) add(key string, f schemas.ReviewFinding) {
	if _, ok := g.m[key]; !ok {
		g.keys = append(g.keys, key)
	}
	g.m[key] = append(g.m[key], f)
}

func findingsForTitles(titles []string, byTitle map[string]schemas.ReviewFinding) []schemas.ReviewFinding {
	out := make([]schemas.ReviewFinding, 0, len(titles))
	for _, t := range titles {
		if f, ok := byTitle[t]; ok {
			out = append(out, f)
		}
	}
	return out
}

func sortedKeys(m map[string]map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(s map[string]struct{}) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pyDirname ports posixpath.dirname.
func pyDirname(p string) string {
	i := strings.LastIndex(p, "/") + 1
	head := p[:i]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}
