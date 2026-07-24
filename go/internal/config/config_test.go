package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// configEnvKeys is every env var this package reads. clearConfigEnv unsets them
// all (and restores on cleanup) so a table-driven env test starts from a known
// blank slate — t.Setenv can only set, not unset, and strEnv/LookupEnv treat a
// present-but-empty value differently from an absent one.
var configEnvKeys = []string{
	"PR_AF_PROVIDER", "PR_AF_MODEL", "PR_AF_AI_MODEL",
	"PR_AF_MAX_TURNS", "PR_AF_AI_MAX_RETRIES",
	"PR_AF_AI_INITIAL_BACKOFF_SECONDS", "PR_AF_AI_MAX_BACKOFF_SECONDS",
	"PR_AF_OPENCODE_BIN", "PR_AF_HARNESS_BIN", "PR_AF_OPENCODE_SERVER",
	"PR_AF_MAX_COST_USD", "PR_AF_MAX_DURATION_SECONDS",
	"PR_AF_EVIDENCE_PACK", "PR_AF_POSTWORTHINESS_GATE",
	"HAX_API_KEY", "AGENTFIELD_APPROVAL_USER_ID",
	"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
	"GOOGLE_API_KEY", "GH_TOKEN", "XDG_DATA_HOME",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range configEnvKeys {
		k := k
		if prev, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, prev) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
		_ = os.Unsetenv(k)
	}
}

func ptrF(f float64) *float64 { return &f }
func ptrI(i int) *int         { return &i }

// ---------------------------------------------------------------------------
// V7 — Budget cap resolution: explicit arg wins > env > defaults (2.0/300).
// ---------------------------------------------------------------------------

// mustAIConfig / mustFromInput unwrap the error-returning constructors for
// tests whose env is known-wellformed.
func mustAIConfig(t *testing.T) AIIntegrationConfig {
	t.Helper()
	c, err := AIConfigFromEnv()
	if err != nil {
		t.Fatalf("AIConfigFromEnv: %v", err)
	}
	return c
}

func mustFromInput(t *testing.T, in schemas.ReviewInput) ReviewConfig {
	t.Helper()
	c, err := ReviewConfig{}.FromInput(in)
	if err != nil {
		t.Fatalf("FromInput: %v", err)
	}
	return c
}

func TestResolveBudgetCapsCascade(t *testing.T) {
	cases := []struct {
		name     string
		argCost  *float64
		argDur   *int
		envCost  string // "" means leave unset
		envDur   string
		wantCost float64
		wantDur  int
	}{
		{"defaults when nil and no env", nil, nil, "", "", 2.0, 3600},
		{"env used when arg nil", nil, nil, "3.3", "450", 3.3, 450},
		{"explicit arg wins over env", ptrF(5.0), ptrI(120), "3.3", "450", 5.0, 120},
		{"explicit arg wins over defaults", ptrF(1.25), ptrI(90), "", "", 1.25, 90},
		{"mixed: explicit cost, env duration", ptrF(9.0), nil, "3.3", "450", 9.0, 450},
		{"mixed: env cost, explicit duration", nil, ptrI(77), "3.3", "450", 3.3, 77},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearConfigEnv(t)
			if c.envCost != "" {
				t.Setenv("PR_AF_MAX_COST_USD", c.envCost)
			}
			if c.envDur != "" {
				t.Setenv("PR_AF_MAX_DURATION_SECONDS", c.envDur)
			}
			gotCost, gotDur, err := ResolveBudgetCaps(c.argCost, c.argDur)
			if err != nil {
				t.Fatalf("ResolveBudgetCaps: %v", err)
			}
			if gotCost != c.wantCost || gotDur != c.wantDur {
				t.Errorf("ResolveBudgetCaps = (%v, %d), want (%v, %d)", gotCost, gotDur, c.wantCost, c.wantDur)
			}
		})
	}

	// Malformed env raises, exactly like Python's float()/int() inside
	// _resolve_budget_caps (ValueError -> HTTP 400 at the node layer), with
	// Python's message shape. It must NOT silently fall back to the default.
	t.Run("unparseable env is an error with the Python message", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("PR_AF_MAX_COST_USD", "abc")
		_, _, err := ResolveBudgetCaps(nil, nil)
		if err == nil || err.Error() != "could not convert string to float: 'abc'" {
			t.Fatalf("cost err = %v, want Python float() message", err)
		}
		clearConfigEnv(t)
		t.Setenv("PR_AF_MAX_DURATION_SECONDS", "xyz")
		_, _, err = ResolveBudgetCaps(nil, nil)
		if err == nil || err.Error() != "invalid literal for int() with base 10: 'xyz'" {
			t.Fatalf("duration err = %v, want Python int() message", err)
		}
		// And the boot-time path: a malformed PR_AF_MAX_TURNS fails
		// AIConfigFromEnv (Python crashes at import).
		clearConfigEnv(t)
		t.Setenv("PR_AF_MAX_TURNS", "many")
		if _, err := AIConfigFromEnv(); err == nil {
			t.Fatal("AIConfigFromEnv with PR_AF_MAX_TURNS=many: want error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// AIIntegrationConfig env cascade + provider_env.
// ---------------------------------------------------------------------------

func TestAIConfigFromEnvDefaults(t *testing.T) {
	clearConfigEnv(t)
	c := mustAIConfig(t)
	if c.Provider != "opencode" {
		t.Errorf("Provider = %q, want opencode", c.Provider)
	}
	// The CODE default is minimax — NOT the manifest's kimi default. env wins,
	// but with no env this must be minimax (design §B.6).
	if c.HarnessModel != "minimax/minimax-m2.5" {
		t.Errorf("HarnessModel = %q, want minimax/minimax-m2.5", c.HarnessModel)
	}
	if c.AIModel != "minimax/minimax-m2.5" {
		t.Errorf("AIModel = %q, want minimax/minimax-m2.5", c.AIModel)
	}
	if c.MaxTurns != 50 || c.MaxRetries != 3 {
		t.Errorf("MaxTurns/MaxRetries = %d/%d, want 50/3", c.MaxTurns, c.MaxRetries)
	}
	if c.InitialBackoffSeconds != 2.0 || c.MaxBackoffSeconds != 8.0 {
		t.Errorf("backoff = %v/%v, want 2.0/8.0", c.InitialBackoffSeconds, c.MaxBackoffSeconds)
	}
	if c.OpencodeBin != "opencode" {
		t.Errorf("OpencodeBin = %q, want opencode", c.OpencodeBin)
	}
	if c.HarnessBin != "" {
		t.Errorf("HarnessBin = %q, want empty when unset", c.HarnessBin)
	}
	if c.OpencodeServer != nil {
		t.Errorf("OpencodeServer = %v, want nil", *c.OpencodeServer)
	}
}

func TestAIConfigFromEnvOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PR_AF_PROVIDER", "claude-code")
	t.Setenv("PR_AF_MODEL", "openrouter/moonshotai/kimi-k2.5")
	t.Setenv("PR_AF_MAX_TURNS", "12")
	t.Setenv("PR_AF_AI_MAX_RETRIES", "6")
	t.Setenv("PR_AF_AI_INITIAL_BACKOFF_SECONDS", "1.5")
	t.Setenv("PR_AF_AI_MAX_BACKOFF_SECONDS", "16")
	t.Setenv("PR_AF_OPENCODE_BIN", "/usr/bin/opencode")
	t.Setenv("PR_AF_HARNESS_BIN", "C:/bin/provider-custom")
	t.Setenv("PR_AF_OPENCODE_SERVER", "http://localhost:9000")

	c := mustAIConfig(t)
	if c.Provider != "claude-code" {
		t.Errorf("Provider = %q", c.Provider)
	}
	if c.HarnessModel != "openrouter/moonshotai/kimi-k2.5" {
		t.Errorf("HarnessModel = %q", c.HarnessModel)
	}
	// AIModel falls back to PR_AF_MODEL when PR_AF_AI_MODEL is unset.
	if c.AIModel != "openrouter/moonshotai/kimi-k2.5" {
		t.Errorf("AIModel fallback = %q, want the PR_AF_MODEL value", c.AIModel)
	}
	if c.MaxTurns != 12 || c.MaxRetries != 6 {
		t.Errorf("MaxTurns/MaxRetries = %d/%d, want 12/6", c.MaxTurns, c.MaxRetries)
	}
	if c.InitialBackoffSeconds != 1.5 || c.MaxBackoffSeconds != 16 {
		t.Errorf("backoff = %v/%v", c.InitialBackoffSeconds, c.MaxBackoffSeconds)
	}
	if c.OpencodeServer == nil || *c.OpencodeServer != "http://localhost:9000" {
		t.Errorf("OpencodeServer = %v", c.OpencodeServer)
	}
	if c.HarnessBin != "C:/bin/provider-custom" {
		t.Errorf("HarnessBin = %q, want exact configured value", c.HarnessBin)
	}

	// PR_AF_AI_MODEL, when set, wins over PR_AF_MODEL for AIModel only.
	t.Setenv("PR_AF_AI_MODEL", "anthropic/claude-x")
	c2 := mustAIConfig(t)
	if c2.AIModel != "anthropic/claude-x" {
		t.Errorf("AIModel = %q, want anthropic/claude-x", c2.AIModel)
	}
	if c2.HarnessModel != "openrouter/moonshotai/kimi-k2.5" {
		t.Errorf("HarnessModel should not be affected by PR_AF_AI_MODEL: %q", c2.HarnessModel)
	}
}

func TestAIConfigFromEnvEmptyHarnessBinIsNoOverride(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PR_AF_HARNESS_BIN", "")
	if got := mustAIConfig(t).HarnessBin; got != "" {
		t.Errorf("HarnessBin = %q, want empty for explicit empty environment value", got)
	}
}

func TestProviderEnv(t *testing.T) {
	clearConfigEnv(t)
	xdg := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("GH_TOKEN", "gh-tok")
	t.Setenv("XDG_DATA_HOME", xdg)

	env := mustAIConfig(t).ProviderEnv()
	if env["OPENROUTER_API_KEY"] != "or-key" {
		t.Errorf("OPENROUTER_API_KEY = %q", env["OPENROUTER_API_KEY"])
	}
	if env["GH_TOKEN"] != "gh-tok" {
		t.Errorf("GH_TOKEN = %q", env["GH_TOKEN"])
	}
	// Unset credentials must not be forwarded.
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("ANTHROPIC_API_KEY should be absent")
	}
	if env["XDG_DATA_HOME"] != xdg {
		t.Errorf("XDG_DATA_HOME = %q, want %q", env["XDG_DATA_HOME"], xdg)
	}

	// With XDG_DATA_HOME unset, ProviderEnv falls back to a tmp dir and creates
	// it.
	t.Setenv("XDG_DATA_HOME", "")
	_ = os.Unsetenv("XDG_DATA_HOME")
	env2 := mustAIConfig(t).ProviderEnv()
	wantXDG := filepath.Join(os.TempDir(), "opencode-shared-data")
	if env2["XDG_DATA_HOME"] != wantXDG {
		t.Errorf("fallback XDG_DATA_HOME = %q, want %q", env2["XDG_DATA_HOME"], wantXDG)
	}
	if st, err := os.Stat(wantXDG); err != nil || !st.IsDir() {
		t.Errorf("fallback XDG dir not created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BudgetConfig / evidence-pack + numeric table.
// ---------------------------------------------------------------------------

func TestDefaultBudgetConfig(t *testing.T) {
	clearConfigEnv(t)
	b := DefaultBudgetConfig()
	if b.MaxCostUSD != 2.0 || b.MaxDurationSeconds != 3600 {
		t.Errorf("caps = %v/%d, want 2.0/3600", b.MaxCostUSD, b.MaxDurationSeconds)
	}
	if b.MaxConcurrentReviewers != 8 || b.MaxReferenceFollowsPerReviewer != 3 ||
		b.MaxChildSpawnsPerReviewer != 2 || b.MaxCrossRefDeepDives != 5 ||
		b.MaxCoverageIterations != 2 || b.MaxReviewDepth != 2 {
		t.Errorf("loop caps mismatch: %+v", b)
	}
	if !b.EvidencePackReviewers {
		t.Errorf("EvidencePackReviewers = false, want true (default ON)")
	}
	wantPhases := map[string]float64{
		"intake": 0.05, "anatomy": 0.15, "meta_selectors": 0.30, "review": 0.90,
		"adversary": 0.40, "cross_ref": 0.30, "coverage": 0.10, "synthesis": 0.0, "output": 0.0,
	}
	if !reflect.DeepEqual(b.PhaseBudgets, wantPhases) {
		t.Errorf("PhaseBudgets = %v, want %v", b.PhaseBudgets, wantPhases)
	}
}

func TestEvidencePackToggle(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"0", false}, {"false", false}, {"no", false}, {"FALSE", false},
		{"1", true}, {"true", true}, {"yes", true}, {"anything", true},
	} {
		t.Run(tc.val, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("PR_AF_EVIDENCE_PACK", tc.val)
			if got := DefaultBudgetConfig().EvidencePackReviewers; got != tc.want {
				t.Errorf("PR_AF_EVIDENCE_PACK=%q -> %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CommentConfig / post-worthiness gate.
// ---------------------------------------------------------------------------

func TestDefaultCommentConfig(t *testing.T) {
	clearConfigEnv(t)
	c := DefaultCommentConfig()
	if c.MinSeverity != "nitpick" || c.MaxComments != 25 {
		t.Errorf("MinSeverity/MaxComments = %q/%d", c.MinSeverity, c.MaxComments)
	}
	if !c.IncludeSuggestions || !c.IncludeDimensionAttribution || !c.IncludeConfidence {
		t.Errorf("include flags = %+v, want all true", c)
	}
	if c.SuggestionMode != "comment" || !c.PolishEnabled || !c.MergeGateEnabled {
		t.Errorf("suggestion/polish/merge = %+v", c)
	}
	if c.PostWorthinessGate {
		t.Errorf("PostWorthinessGate = true, want false (default OFF)")
	}
	if c.SeverityEmojis["critical"] != "🔴" || c.SeverityEmojis["nitpick"] != "⚪" {
		t.Errorf("SeverityEmojis = %v", c.SeverityEmojis)
	}
}

func TestPostWorthinessToggle(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"1", true}, {"true", true}, {"yes", true}, {"YES", true},
		{"0", false}, {"false", false}, {"", false}, {"maybe", false},
	} {
		t.Run("v_"+tc.val, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("PR_AF_POSTWORTHINESS_GATE", tc.val)
			if got := DefaultCommentConfig().PostWorthinessGate; got != tc.want {
				t.Errorf("PR_AF_POSTWORTHINESS_GATE=%q -> %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HITLConfig.
// ---------------------------------------------------------------------------

func TestDefaultHITLConfig(t *testing.T) {
	clearConfigEnv(t)
	h := DefaultHITLConfig()
	if h.Enabled {
		t.Errorf("Enabled = true, want false (no HAX_API_KEY)")
	}
	if h.ApprovalUserID != nil {
		t.Errorf("ApprovalUserID = %v, want nil", *h.ApprovalUserID)
	}
	if h.ApprovalExpiresInHours != 72 || h.MaxReviewRevisions != 2 {
		t.Errorf("caps = %d/%d, want 72/2", h.ApprovalExpiresInHours, h.MaxReviewRevisions)
	}

	// HAX_API_KEY set (non-blank) enables HITL; blank whitespace does not.
	clearConfigEnv(t)
	t.Setenv("HAX_API_KEY", "secret")
	t.Setenv("AGENTFIELD_APPROVAL_USER_ID", "user-9")
	h2 := DefaultHITLConfig()
	if !h2.Enabled {
		t.Errorf("Enabled = false with HAX_API_KEY set")
	}
	if h2.ApprovalUserID == nil || *h2.ApprovalUserID != "user-9" {
		t.Errorf("ApprovalUserID = %v, want user-9", h2.ApprovalUserID)
	}

	clearConfigEnv(t)
	t.Setenv("HAX_API_KEY", "   ")
	if DefaultHITLConfig().Enabled {
		t.Errorf("whitespace HAX_API_KEY should not enable HITL")
	}
}

// ---------------------------------------------------------------------------
// Depth profiles + thresholds (static).
// ---------------------------------------------------------------------------

func TestDepthProfiles(t *testing.T) {
	want := map[string]DepthProfile{
		"quick":    {MaxDimensions: 3, ModelTier: "budget"},
		"standard": {MaxDimensions: 6, ModelTier: "standard"},
		"deep":     {MaxDimensions: 12, ModelTier: "premium"},
	}
	if !reflect.DeepEqual(DepthProfiles, want) {
		t.Errorf("DepthProfiles = %v, want %v", DepthProfiles, want)
	}
	wantThresholds := []AutoDepthThreshold{{100, "quick"}, {500, "standard"}}
	if !reflect.DeepEqual(AutoDepthThresholds, wantThresholds) {
		t.Errorf("AutoDepthThresholds = %v, want %v", AutoDepthThresholds, wantThresholds)
	}
}

// ---------------------------------------------------------------------------
// ReviewConfig.FromInput merge semantics.
// ---------------------------------------------------------------------------

func TestDefaultReviewConfigIgnorePaths(t *testing.T) {
	clearConfigEnv(t)
	c := DefaultReviewConfig()
	if len(c.IgnorePaths) != 11 {
		t.Errorf("IgnorePaths len = %d, want 11", len(c.IgnorePaths))
	}
	if c.Hints == nil {
		t.Errorf("Hints = nil, want non-nil empty")
	}
}

func TestFromInputBudgetCapsResolvedToPerCallDefault(t *testing.T) {
	// Key parity check: with no explicit caps and no env, FromInput must set the
	// per-call duration to 3600 via the ResolveBudgetCaps cascade — the same
	// resolved value Python's review() always writes over the BudgetConfig
	// default.
	clearConfigEnv(t)
	c := mustFromInput(t, schemas.ReviewInput{MaxReviewDepth: 2})
	if c.Budget.MaxCostUSD != 2.0 {
		t.Errorf("MaxCostUSD = %v, want 2.0", c.Budget.MaxCostUSD)
	}
	if c.Budget.MaxDurationSeconds != 3600 {
		t.Errorf("MaxDurationSeconds = %d, want 3600 (per-call resolved default)", c.Budget.MaxDurationSeconds)
	}
}

func TestFromInputMergeSemantics(t *testing.T) {
	clearConfigEnv(t)
	in := schemas.ReviewInput{
		MaxCostUSD:             ptrF(7.5),
		MaxDurationSeconds:     ptrI(150),
		MaxConcurrentReviewers: ptrI(4),
		MaxCoverageIterations:  ptrI(5),
		MaxReviewDepth:         10, // clamps to 3
		Models:                 map[string]string{"reviewer": "anthropic/claude-x", "bogus_field": "ignored"},
		IgnorePaths:            []string{"custom/**", "*.md"}, // *.md dups a default
		Hints:                  []string{"be strict", "check nil derefs"},
		SuggestionMode:         "code",
	}
	c := mustFromInput(t, in)

	if c.Budget.MaxCostUSD != 7.5 || c.Budget.MaxDurationSeconds != 150 {
		t.Errorf("explicit caps = %v/%d, want 7.5/150", c.Budget.MaxCostUSD, c.Budget.MaxDurationSeconds)
	}
	if c.Budget.MaxConcurrentReviewers != 4 {
		t.Errorf("MaxConcurrentReviewers = %d, want 4", c.Budget.MaxConcurrentReviewers)
	}
	if c.Budget.MaxCoverageIterations != 5 {
		t.Errorf("MaxCoverageIterations = %d, want 5", c.Budget.MaxCoverageIterations)
	}
	if c.Budget.MaxReviewDepth != 3 {
		t.Errorf("MaxReviewDepth = %d, want 3 (clamped from 10)", c.Budget.MaxReviewDepth)
	}
	if c.Model.Reviewer != "anthropic/claude-x" {
		t.Errorf("Model.Reviewer = %q, want anthropic/claude-x", c.Model.Reviewer)
	}
	// Unknown model key ignored; other fields keep defaults.
	if c.Model.Planner != "premium" {
		t.Errorf("Model.Planner = %q, want premium (unchanged)", c.Model.Planner)
	}
	// ignore_paths union deduped: *.md appears once, custom/** present.
	if countOf(c.IgnorePaths, "*.md") != 1 {
		t.Errorf("*.md count = %d, want 1 (deduped)", countOf(c.IgnorePaths, "*.md"))
	}
	if countOf(c.IgnorePaths, "custom/**") != 1 {
		t.Errorf("custom/** missing from union: %v", c.IgnorePaths)
	}
	if len(c.IgnorePaths) != 12 { // 11 defaults + custom/** (*.md deduped)
		t.Errorf("IgnorePaths len = %d, want 12", len(c.IgnorePaths))
	}
	if !reflect.DeepEqual(c.Hints, []string{"be strict", "check nil derefs"}) {
		t.Errorf("Hints = %v", c.Hints)
	}
	if c.Comments.SuggestionMode != "code" {
		t.Errorf("SuggestionMode = %q, want code", c.Comments.SuggestionMode)
	}
}

func TestFromInputReviewDepthClampVariants(t *testing.T) {
	clearConfigEnv(t)
	for _, tc := range []struct{ in, want int }{
		{1, 1}, {2, 2}, {3, 3}, {4, 3}, {99, 3},
	} {
		c := mustFromInput(t, schemas.ReviewInput{MaxReviewDepth: tc.in})
		if c.Budget.MaxReviewDepth != tc.want {
			t.Errorf("FromInput MaxReviewDepth(%d) = %d, want %d", tc.in, c.Budget.MaxReviewDepth, tc.want)
		}
	}
}

func TestFromInputEmptyOverridesKeepDefaults(t *testing.T) {
	clearConfigEnv(t)
	// Empty hints / suggestion_mode leave the defaults intact.
	c := mustFromInput(t, schemas.ReviewInput{MaxReviewDepth: 2, Hints: nil, SuggestionMode: ""})
	if len(c.Hints) != 0 {
		t.Errorf("Hints = %v, want empty (unchanged)", c.Hints)
	}
	if c.Comments.SuggestionMode != "comment" {
		t.Errorf("SuggestionMode = %q, want comment (unchanged)", c.Comments.SuggestionMode)
	}
	if len(c.IgnorePaths) != 11 {
		t.Errorf("IgnorePaths = %d, want 11 (no union)", len(c.IgnorePaths))
	}
}

func countOf(xs []string, v string) int {
	n := 0
	for _, x := range xs {
		if x == v {
			n++
		}
	}
	return n
}
