package harnessx

import (
	"context"
	"fmt"

	"github.com/Agent-Field/agentfield/sdk/go/harness"

	"github.com/Agent-Field/pr-af/go/internal/fatal"
)

// HarnessCaller is the minimal method set Run needs from *agent.Agent. Declaring
// it as an interface (rather than depending on the concrete *agent.Agent) lets
// tests supply a mock harness without a live subprocess — the same seam the
// Python tests get by patching the router's harness. *agent.Agent satisfies it
// via its Harness method (sdk/go/agent/harness.go).
type HarnessCaller interface {
	Harness(ctx context.Context, prompt string, schema map[string]any, dest any, opts harness.Options) (*harness.Result, error)
}

// Run is the single generic entry point every reasoner uses to invoke the
// harness for structured output of type T.
//
// Sequence (design §C.3):
//  1. Reflect T into the JSON schema the harness consumes (cached per type).
//  2. Call app.Harness with a fresh *T dest. PR-AF has no scout-credential
//     store, so — unlike the SWE-AF harness this is adapted from — Run does NOT
//     inject run-scoped credentials into opts.Env; opts is passed through as-is.
//  3. Classify fatal (non-retryable) API errors FIRST, before the structured-
//     output error check, so the real billing/auth message surfaces past every
//     retry layer as a *fatal.FatalHarnessError.
//  4. Reject a nil, error, or unparsed Result as StructuredOutputError. A caller
//     may deliberately classify that error as an explicit degraded result, but
//     it cannot mistake a missing structured response for a successful value.
//
// Returns (*T, *harness.Result, error). The Result is returned even alongside a
// non-nil error so callers can inspect diagnostics.
func Run[T any](ctx context.Context, app HarnessCaller, prompt string, opts harness.Options) (*T, *harness.Result, error) {
	schema := schemaFor[T]()

	var dest T
	result, err := app.Harness(ctx, prompt, schema, &dest, opts)
	if err != nil {
		return nil, result, err
	}

	// Fatal-error classification comes before the structured-output check so the
	// real non-retryable message is not masked by a generic parse diagnostic.
	if fErr := fatal.CheckFatalHarnessError(result); fErr != nil {
		return nil, result, fErr
	}

	if result == nil {
		return nil, nil, &StructuredOutputError{}
	}
	if result.IsError || result.Parsed == nil {
		return nil, result, &StructuredOutputError{Diagnostic: result.ErrorMessage}
	}

	return &dest, result, nil
}

// StructuredOutputError identifies a harness call that completed without a
// usable schema-validated result.
type StructuredOutputError struct {
	Diagnostic string
}

// Error returns a stable message while retaining the harness diagnostic for
// logs and tests.
func (e *StructuredOutputError) Error() string {
	if e.Diagnostic == "" {
		return "harness returned no schema-validated structured output"
	}
	return fmt.Sprintf("harness returned no schema-validated structured output: %s", e.Diagnostic)
}

// RoleOptions is the role→harness parameter mapping (design §C.3). Each reasoner
// fills it from its resolved config, then ToOptions produces the harness.Options
// passed to Run. Centralizing the mapping here keeps every reasoner consistent —
// the field set mirrors the keyword arguments the Python reasoners pass to
// router.harness (system_prompt, schema, model, provider, tools, cwd, max_turns,
// permission_mode).
type RoleOptions struct {
	// Provider is the harness ADAPTER string, e.g. "aforge" (PR-AF's default) or "opencode",
	// "claude-code", "codex".
	Provider string

	// Model is the resolved role model identifier.
	Model string

	// MaxTurns caps agent iterations.
	MaxTurns int

	// Tools is the allowed-tool list (e.g. ["Read","Grep","Bash"]).
	Tools []string

	// PermissionMode maps to Python's permission_mode; empty means the harness
	// default (Python passes `permission_mode or None`, and harness.Options
	// treats "" as "use default").
	PermissionMode string

	// SystemPrompt is the role's module-level system prompt.
	SystemPrompt string

	// Cwd is the working directory for the subprocess (repo path / worktree).
	Cwd string

	// Env is the environment for the subprocess. Run passes it through unchanged.
	Env map[string]string
}

// ToOptions converts a RoleOptions into a harness.Options.
func (r RoleOptions) ToOptions() harness.Options {
	return harness.Options{
		Provider:       r.Provider,
		Model:          r.Model,
		MaxTurns:       r.MaxTurns,
		Tools:          r.Tools,
		PermissionMode: r.PermissionMode,
		SystemPrompt:   r.SystemPrompt,
		Cwd:            r.Cwd,
		Env:            r.Env,
	}
}
