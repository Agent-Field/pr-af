// Package config ports PR-AF's config.py plus the app.py:62-77
// _resolve_budget_caps cascade. Every env var is read at CALL time (inside the
// FromEnv / default constructors), never at package init, so a t.Setenv in a
// test is deterministic and no value is frozen at import.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AIIntegrationConfig ports config.py AIIntegrationConfig. Env precedence and
// defaults per design §C.2. The HarnessModel/AIModel fallback is the CODE
// default "minimax/minimax-m2.5" — deliberately different from the
// Docker/compose/manifest default (openrouter/moonshotai/kimi-k2.5); env always
// wins, and both facts ship intentionally (design §B.6). Do not "fix" it.
type AIIntegrationConfig struct {
	Provider              string  `json:"provider"`
	HarnessModel          string  `json:"harness_model"`
	AIModel               string  `json:"ai_model"`
	MaxTurns              int     `json:"max_turns"`
	MaxRetries            int     `json:"max_retries"`
	InitialBackoffSeconds float64 `json:"initial_backoff_seconds"`
	MaxBackoffSeconds     float64 `json:"max_backoff_seconds"`
	OpencodeBin           string  `json:"opencode_bin"`
	OpencodeServer        *string `json:"opencode_server"`
}

// AIConfigFromEnv resolves the AI integration config from the environment
// (config.py AIIntegrationConfig.from_env / its default_factory lambdas).
func AIConfigFromEnv() AIIntegrationConfig {
	return AIIntegrationConfig{
		Provider:     strEnv("PR_AF_PROVIDER", "opencode"),
		HarnessModel: strEnv("PR_AF_MODEL", "minimax/minimax-m2.5"),
		// AI_MODEL falls back to PR_AF_MODEL, then to the code default.
		AIModel:               strEnv("PR_AF_AI_MODEL", strEnv("PR_AF_MODEL", "minimax/minimax-m2.5")),
		MaxTurns:              intEnv("PR_AF_MAX_TURNS", 50),
		MaxRetries:            intEnv("PR_AF_AI_MAX_RETRIES", 3),
		InitialBackoffSeconds: floatEnv("PR_AF_AI_INITIAL_BACKOFF_SECONDS", 2.0),
		MaxBackoffSeconds:     floatEnv("PR_AF_AI_MAX_BACKOFF_SECONDS", 8.0),
		OpencodeBin:           strEnv("PR_AF_OPENCODE_BIN", "opencode"),
		OpencodeServer:        lookupPtr("PR_AF_OPENCODE_SERVER"),
	}
}

// ProviderEnv builds the subprocess environment forwarded to the opencode
// harness (config.py provider_env): the LLM/GitHub credentials that are set,
// plus an XDG_DATA_HOME that is created if missing.
func (c AIIntegrationConfig) ProviderEnv() map[string]string {
	env := map[string]string{}
	for _, key := range []string{
		"OPENROUTER_API_KEY",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"GOOGLE_API_KEY",
		"GH_TOKEN",
	} {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		xdg = filepath.Join(os.TempDir(), "opencode-shared-data")
	}
	_ = os.MkdirAll(xdg, 0o755)
	env["XDG_DATA_HOME"] = xdg
	return env
}

// --- shared env readers (call-time only) ---

// strEnv returns the env value for key, or def when the key is unset. A key that
// is set (even to "") returns its value, matching Python's os.getenv(key, def).
func strEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// intEnv parses key as an int, falling back to def when unset or unparseable.
func intEnv(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

// floatEnv parses key as a float64, falling back to def when unset or
// unparseable. (Python's float(os.getenv(...)) would raise on a bad value; the
// (float64,int)-returning ResolveBudgetCaps signature has no error channel, so
// we fall back instead of panicking.)
func floatEnv(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}

// lookupPtr returns a pointer to the env value when the key is present (even if
// ""), or nil when unset — the Go analog of os.getenv returning None.
func lookupPtr(key string) *string {
	if v, ok := os.LookupEnv(key); ok {
		return &v
	}
	return nil
}
