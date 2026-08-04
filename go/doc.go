// Package praf is the module root marker for the PR-AF Go port.
//
// The port turns a pull request (or raw diff / local repo) into a structured,
// multi-dimensional code review — the same reasoner names, control-plane
// surface, and HTTP API shapes as the Python pr_af package. It registers as an
// maintained node ("pr-af", default port 8007). NODE_ID can be overridden when
// it runs alongside the untouched Python node.
//
// Layout (design §A):
//
//	cmd/pr-af          node entry point (node id "pr-af")
//	internal/afx       small AgentField SDK ergonomics (typed input binding)
//	internal/fatal     non-retryable harness-error classification
//	internal/harnessx  the single generic Run[T] harness choke-point + schema cache
//	internal/...       schemas, config, prompts, reasoners, orchestrator, etc.
//
// It depends on the AgentField Go SDK
// (github.com/Agent-Field/agentfield/sdk/go), consumed via a REAL versioned
// require resolved from proxy.golang.org — there is NO replace directive. A
// gitignored go.work workspace is the local dev path (design §A, §E.1).
package praf
