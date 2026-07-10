package reasoners

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// This file ports the deterministic (non-LLM) helpers from
// reasoners/harnesses.py. Where Python iterates a dict for its insertion
// order (ext_map, area_patterns), the Go port uses an ordered slice of pairs —
// iteration order is part of the observable behavior (first-match wins for
// languages, output order for areas).

// autoDepth ports _auto_depth: complexity -> review depth, default "standard".
func autoDepth(complexity string) string {
	switch complexity {
	case "trivial":
		return "quick"
	case "standard":
		return "standard"
	case "complex", "massive":
		return "deep"
	default:
		return "standard"
	}
}

// extPair keeps _language_from_path's ext_map insertion order (first suffix
// match wins, exactly like the Python dict iteration).
type extPair struct{ ext, language string }

var extMap = []extPair{
	{".py", "python"},
	{".js", "javascript"},
	{".jsx", "javascript"},
	{".ts", "typescript"},
	{".tsx", "typescript"},
	{".go", "go"},
	{".rs", "rust"},
	{".java", "java"},
	{".rb", "ruby"},
	{".php", "php"},
	{".swift", "swift"},
	{".kt", "kotlin"},
	{".cs", "csharp"},
	{".cpp", "cpp"},
	{".c", "c"},
	{".sql", "sql"},
	{".html", "html"},
	{".css", "css"},
	{".md", "markdown"},
	{".json", "json"},
	{".yaml", "yaml"},
	{".yml", "yaml"},
	{".sh", "bash"},
}

// languageFromPath ports _language_from_path.
func languageFromPath(path string) string {
	for _, p := range extMap {
		if strings.HasSuffix(path, p.ext) {
			return p.language
		}
	}
	return ""
}

// extractLanguages ports _extract_languages: the sorted set of non-empty
// languages across the PR's changed files.
func extractLanguages(pr schemas.GitHubPRData) []string {
	set := map[string]struct{}{}
	for _, changed := range pr.ChangedFiles {
		if lang := languageFromPath(changed.Path); lang != "" {
			set[lang] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for lang := range set {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// writeContextFile ports _write_context_file: writes large context under
// <repo>/.pr-af-context/<name> for the harness subprocess to read, returning
// the file path. Errors propagate (Python lets the OSError escape).
func writeContextFile(content, name, repoPath string) (string, error) {
	ctxDir := filepath.Join(repoPath, ".pr-af-context")
	if err := os.MkdirAll(ctxDir, 0o777); err != nil {
		return "", err
	}
	path := filepath.Join(ctxDir, name)
	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		return "", err
	}
	return path, nil
}

// areaPair keeps _extract_areas' area_patterns insertion order (the detected
// list is emitted in this order).
type areaPair struct {
	area     string
	patterns []string
}

var areaPatterns = []areaPair{
	{"auth", []string{"auth", "login", "oauth", "permission", "acl"}},
	{"database", []string{"db", "database", "migration", "schema", "model"}},
	{"api", []string{"api", "endpoint", "route", "controller", "handler"}},
	{"frontend", []string{"ui", "component", "view", "page", "css", "tsx", "jsx"}},
	{"tests", []string{"test", "spec", "fixture"}},
	{"ci", []string{".github", "workflow", "ci", "pipeline"}},
	{"config", []string{"config", "settings", ".env", "yaml", "toml", "json"}},
	{"infra", []string{"docker", "k8s", "terraform", "helm", "ansible"}},
	{"security", []string{"security", "crypto", "token", "jwt", "secret"}},
}

// extractAreas ports _extract_areas; empty detection falls back to
// ["application"].
func extractAreas(paths []string) []string {
	lowered := make([]string, len(paths))
	for i, p := range paths {
		lowered[i] = strings.ToLower(p)
	}
	detected := []string{}
	for _, ap := range areaPatterns {
		hit := false
	scan:
		for _, path := range lowered {
			for _, pattern := range ap.patterns {
				if strings.Contains(path, pattern) {
					hit = true
					break scan
				}
			}
		}
		if hit {
			detected = append(detected, ap.area)
		}
	}
	if len(detected) == 0 {
		detected = append(detected, "application")
	}
	return detected
}

// riskSignals ports _risk_signals (signal order fixed by the Python code).
func riskSignals(pr schemas.GitHubPRData, areasTouched []string, filesChanged int) []string {
	signals := []string{}
	has := func(area string) bool {
		for _, a := range areasTouched {
			if a == area {
				return true
			}
		}
		return false
	}
	if has("security") || has("auth") {
		signals = append(signals, "touches authentication or security-sensitive paths")
	}
	if has("database") {
		signals = append(signals, "modifies data model or schema-affecting code")
	}
	if has("api") {
		signals = append(signals, "changes API surface or request/response behavior")
	}
	if filesChanged >= 25 {
		signals = append(signals, "large change footprint across many files")
	}
	configChange := false
	for _, cf := range pr.ChangedFiles {
		if strings.HasSuffix(cf.Path, ".yml") || strings.HasSuffix(cf.Path, ".yaml") ||
			strings.HasSuffix(cf.Path, ".toml") || strings.HasSuffix(cf.Path, ".json") {
			configChange = true
			break
		}
	}
	if configChange {
		signals = append(signals, "includes configuration changes")
	}
	testChange := false
	for _, cf := range pr.ChangedFiles {
		if strings.Contains(strings.ToLower(cf.Path), "test") {
			testChange = true
			break
		}
	}
	if testChange {
		signals = append(signals, "test behavior updated")
	}
	return signals
}

// aiGeneratedPatterns is _ai_generated_confidence's substring pattern tuple.
var aiGeneratedPatterns = []string{
	"generated by",
	"co-authored-by: claude",
	"co-authored-by: gpt",
	"ai-assisted",
	"autogenerated",
	"chatgpt",
	"copilot",
	"claude",
	"llm",
}

// aiGeneratedConfidence ports _ai_generated_confidence: the fraction of
// non-empty text blobs (title, description, commit messages) that carry an
// AI-generation marker, capped at 1.0. Zero non-empty blobs -> 0.0.
func aiGeneratedConfidence(pr schemas.GitHubPRData) float64 {
	signals := 0
	evidence := 0
	blobs := append([]string{pr.Title, pr.Description}, pr.CommitMessages...)
	for _, blob := range blobs {
		if blob == "" {
			continue
		}
		evidence++
		lower := strings.ToLower(blob)
		for _, pattern := range aiGeneratedPatterns {
			if strings.Contains(lower, pattern) {
				signals++
				break
			}
		}
	}
	if evidence == 0 {
		return 0.0
	}
	ratio := float64(signals) / float64(evidence)
	if ratio > 1.0 {
		return 1.0
	}
	return ratio
}

// prSummary ports _pr_summary: the stripped description, else
// "<title>. Files changed: <n>.".
func prSummary(pr schemas.GitHubPRData) string {
	description := strings.TrimSpace(pr.Description)
	if description != "" {
		return description
	}
	return pr.Title + ". Files changed: " + itoa(len(pr.ChangedFiles)) + "."
}

// fileChangesFromMetadata ports _file_changes_from_metadata: FileChange stubs
// built from the GitHub /files metadata when the diff text yields nothing.
func fileChangesFromMetadata(pr schemas.GitHubPRData) []schemas.FileChange {
	out := make([]schemas.FileChange, 0, len(pr.ChangedFiles))
	for _, changed := range pr.ChangedFiles {
		out = append(out, schemas.FileChange{
			Path:         changed.Path,
			Status:       changed.Status,
			Language:     languageFromPath(changed.Path),
			LinesAdded:   changed.Additions,
			LinesRemoved: changed.Deletions,
			Hunks:        []schemas.Hunk{},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Small Python-parity utilities shared by the context assemblies below. These
// intentionally mirror the (package-private) equivalents inside prompts —
// contexts_test.go pins the two copies together by asserting each assembled
// context string appears verbatim inside the corresponding built prompt.
// ---------------------------------------------------------------------------

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// runeCap reproduces Python's s[:n] (code points, not bytes).
func runeCap(s string, n int) string {
	if n < 0 {
		n = 0
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// capN reproduces xs[:n].
func capN[T any](xs []T, n int) []T {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}

// orEmptyStrs reproduces Python's `x or []` truthiness for string lists.
func orEmptyStrs(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// filterPairs ports `{k: v for k, v in (diff_patches or {}).items() if v}`:
// drops empty values, dedups keys (last value wins, first position kept). Must
// stay in lockstep with prompts.filterPatches.
func filterPairs(patches []prompts.StrPair) []prompts.StrPair {
	idx := map[string]int{}
	out := []prompts.StrPair{}
	for _, p := range patches {
		if p.Val == "" {
			continue
		}
		if i, ok := idx[p.Key]; ok {
			out[i].Val = p.Val
			continue
		}
		idx[p.Key] = len(out)
		out = append(out, p)
	}
	return out
}

// renderPatches ports "\n\n".join(f"### {p}\n```diff\n{d}\n```") over the
// first 20 (already filtered) patches. Must stay in lockstep with
// prompts.renderPatchesText.
func renderPatches(patches []prompts.StrPair) string {
	patches = capN(patches, 20)
	parts := make([]string, len(patches))
	for i, p := range patches {
		parts[i] = "### " + p.Key + "\n```diff\n" + p.Val + "\n```"
	}
	return strings.Join(parts, "\n\n")
}

// ---------------------------------------------------------------------------
// Evidence-package plumbing. The reasoners accept evidence packages exactly as
// Python does — dict[str, dict], i.e. EvidencePackage.model_dump() maps (plus
// the orchestrator-injected "verification" sub-dict on the adversary path) —
// and convert them to the insertion-ordered *prompts.OMap the builders need.
// ---------------------------------------------------------------------------

// evidenceKeyOrder is EvidencePackage.model_dump()'s field order, with the
// orchestrator's appended "verification" key last — the insertion order the
// Python dicts carry on the live path.
var evidenceKeyOrder = []string{
	"finding_title",
	"primary_code",
	"caller_snippets",
	"cross_ref_snippets",
	"diff_hunk",
	"import_context",
	"related_code",
	"verification",
}

// verificationKeyOrder is the orchestrator's verification sub-dict insertion
// order (orch.py _run_review_layer).
var verificationKeyOrder = []string{"verified", "actual_behavior", "verification_notes"}

// evidenceOMaps converts a Python-shaped evidence map (dict[str, dict]) to the
// ordered form the prompt builders consume. Returns nil for a nil/empty input
// so `bool(ev_map)` truthiness (adversary's has_evidence) is preserved.
func evidenceOMaps(packages map[string]map[string]any) map[string]*prompts.OMap {
	if len(packages) == 0 {
		return nil
	}
	out := make(map[string]*prompts.OMap, len(packages))
	for title, pkg := range packages {
		out[title] = orderedOMap(pkg, evidenceKeyOrder)
	}
	return out
}

// orderedOMap builds an *prompts.OMap from m, emitting known keys in keyOrder
// first, then any remaining keys sorted (deterministic; unreachable on the
// live path, where the dicts are exactly EvidencePackage dumps).
func orderedOMap(m map[string]any, keyOrder []string) *prompts.OMap {
	o := prompts.NewOMap()
	seen := map[string]struct{}{}
	for _, k := range keyOrder {
		if v, ok := m[k]; ok {
			o.Set(k, omapValue(k, v))
			seen[k] = struct{}{}
		}
	}
	rest := make([]string, 0)
	for k := range m {
		if _, ok := seen[k]; !ok {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		o.Set(k, omapValue(k, m[k]))
	}
	return o
}

// omapValue normalizes a JSON-round-tripped value for OMap storage: []any of
// strings becomes []string (what GetStrSlice reads), nested maps become
// ordered OMaps (verification keys in orchestrator order).
func omapValue(key string, v any) any {
	switch x := v.(type) {
	case []any:
		strs := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return x // mixed list: keep as-is for PyJSON's generic path
			}
			strs = append(strs, s)
		}
		return strs
	case map[string]any:
		if key == "verification" {
			return orderedOMap(x, verificationKeyOrder)
		}
		return orderedOMap(x, nil)
	default:
		return v
	}
}

// ---------------------------------------------------------------------------
// Context assemblies for the three evidence-based reasoners. Each reproduces
// the json.dumps payload its Python reasoner computes (and the Go prompt
// builder recomputes internally), so the reasoner can (a) decide the
// write-to-file branch on the same string and (b) write byte-identical file
// content. contexts_test.go asserts each assembly matches the builder's
// inline rendering.
// ---------------------------------------------------------------------------

// compoundFinderContext reproduces compound_finder_phase's findings_summary:
// json.dumps({"cluster_findings": [...], "cluster_evidence": {...}}).
func compoundFinderContext(findings []schemas.ReviewFinding, evidenceMap map[string]*prompts.OMap) string {
	withContext := make([]*prompts.OMap, 0, 4)
	for _, f := range capN(findings, 4) {
		entry := prompts.NewOMap().
			Set("title", f.Title).
			Set("severity", string(f.Severity)).
			Set("file_path", f.FilePath).
			Set("line_start", f.LineStart).
			Set("line_end", f.LineEnd).
			Set("dimension_name", f.DimensionName).
			Set("body", f.Body).
			Set("evidence", f.Evidence).
			Set("suggestion", f.Suggestion).
			Set("tags", orEmptyStrs(f.Tags))
		if ev, ok := evidenceMap[f.Title]; ok && ev != nil {
			entry.Set("evidence_package", prompts.NewOMap().
				Set("primary_code", runeCap(ev.GetStr("primary_code", ""), 4000)).
				Set("import_context", runeCap(ev.GetStr("import_context", ""), 2500)).
				Set("caller_snippets", capN(ev.GetStrSlice("caller_snippets"), 5)).
				Set("related_code", runeCap(ev.GetStr("related_code", ""), 2500)).
				Set("cross_ref_snippets", capN(ev.GetStrSlice("cross_ref_snippets"), 4)))
		}
		withContext = append(withContext, entry)
	}
	clusterEvidence := prompts.NewOMap()
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
	payload := prompts.NewOMap().
		Set("cluster_findings", withContext).
		Set("cluster_evidence", clusterEvidence)
	return prompts.PyJSON(payload)
}

// evidenceVerifierContext reproduces evidence_verifier's findings_text.
func evidenceVerifierContext(findings []schemas.ReviewFinding, evidenceMap map[string]*prompts.OMap) string {
	payload := make([]*prompts.OMap, 0, len(findings))
	for _, f := range findings {
		entry := prompts.NewOMap().
			Set("title", f.Title).
			Set("severity", string(f.Severity)).
			Set("file_path", f.FilePath).
			Set("line_start", f.LineStart).
			Set("dimension_name", f.DimensionName).
			Set("body", f.Body).
			Set("evidence", f.Evidence).
			Set("confidence", f.Confidence)
		if ev, ok := evidenceMap[f.Title]; ok && ev != nil {
			entry.Set("extracted_code", prompts.NewOMap().
				Set("primary_code", runeCap(ev.GetStr("primary_code", ""), 4000)).
				Set("caller_snippets", capN(ev.GetStrSlice("caller_snippets"), 5)).
				Set("diff_hunk", runeCap(ev.GetStr("diff_hunk", ""), 2000)).
				Set("import_context", ev.GetStr("import_context", "")).
				Set("related_code", runeCap(ev.GetStr("related_code", ""), 2000)).
				Set("cross_ref_snippets", capN(ev.GetStrSlice("cross_ref_snippets"), 3)))
		}
		payload = append(payload, entry)
	}
	return prompts.PyJSON(payload)
}

// adversaryContext reproduces adversary_phase's findings_summary (over the
// first 20 findings).
func adversaryContext(findings []schemas.ReviewFinding, evidenceMap map[string]*prompts.OMap) string {
	withEvidence := make([]*prompts.OMap, 0, 20)
	for _, f := range capN(findings, 20) {
		entry := prompts.NewOMap().
			Set("title", f.Title).
			Set("severity", string(f.Severity)).
			Set("file_path", f.FilePath).
			Set("dimension_name", f.DimensionName).
			Set("body", f.Body).
			Set("evidence", f.Evidence).
			Set("suggestion", f.Suggestion).
			Set("confidence", f.Confidence)
		if ev, ok := evidenceMap[f.Title]; ok && ev != nil {
			entry.Set("ground_truth", prompts.NewOMap().
				Set("primary_code", runeCap(ev.GetStr("primary_code", ""), 3000)).
				Set("caller_snippets", capN(ev.GetStrSlice("caller_snippets"), 5)).
				Set("diff_hunk", runeCap(ev.GetStr("diff_hunk", ""), 2000)).
				Set("import_context", ev.GetStr("import_context", "")).
				Set("related_code", runeCap(ev.GetStr("related_code", ""), 2000)))
		}
		withEvidence = append(withEvidence, entry)
	}
	return prompts.PyJSON(withEvidence)
}
