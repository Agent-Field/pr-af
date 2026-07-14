// Package node is the PR-AF wiring wave (T4.2): it constructs the shared
// *agent.Agent from the environment and registers the exact Python reasoner
// surface (design §B.1) so the Go node is a drop-in opt-in sibling of the Python
// pr-af node.
//
// node.go owns agent construction (env -> agent.Config, mirroring
// src/pr_af/app.py:26-50) plus the custom HTTP server that wraps the SDK handler
// with the /webhook/github route (app.py:365-367). register.go owns the
// per-reasoner registration (the 17 names of §B.1); webhook.go owns the GitHub
// @mention webhook handler.
package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/agentfield/sdk/go/ai"

	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/github"
	"github.com/Agent-Field/pr-af/go/internal/orch"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// Node bundles the constructed agent with the resolved environment config and
// the collaborators the review handler and webhook thread through.
type Node struct {
	// App is the SDK agent. It satisfies orch.App (Harness/AI/Pause/Note) and
	// the reasoner Deps interfaces directly, so register.go points every Deps
	// field at it and Serve mounts App.Handler() as the fallback route.
	App *agent.Agent

	// NodeID is the resolved node id (NODE_ID env, or the pr-af-go default).
	NodeID string

	// AgentFieldServer is the control-plane base URL (AGENTFIELD_SERVER). The
	// webhook fires the async review at "{AgentFieldServer}/api/v1/execute/async/
	// {NodeID}.review" and the HITL gate derives the approval webhook URL from it.
	AgentFieldServer string

	// ListenAddress is the ":port" the custom server binds (":"+PORT).
	ListenAddress string

	// reviewApp is the agent-capability seam the review handler feeds into
	// orch.Deps.App AND emits the pipeline-failure note through. It defaults to
	// App; the error-mapping tests override it with a fake that records notes.
	reviewApp orch.App

	// gh is the GitHub client injected into orch.Deps.GH. Tests that stub
	// runReview leave it unused (nil).
	gh github.Client

	// localCaller is the tracked same-process invocation seam fed into
	// orch.Deps.Local: production points it at App so every pipeline phase is
	// reported to the control plane as a child execution (the review DAG).
	// Tests that stub runReview or need direct-call phases leave it nil.
	localCaller orch.LocalCaller

	// runReview is the orchestrator-construct-and-run seam. Production builds and
	// runs the real orchestrator; the error-mapping tests inject ErrBadInput /
	// other failures without a live harness (design §F "seam for the orchestrator
	// constructor").
	runReview func(ctx context.Context, deps orch.Deps, in schemas.ReviewInput, cfg config.ReviewConfig) (schemas.ReviewResult, error)

	// registered records every reasoner name passed through the single
	// registration path, in order, so the parity test (V1) can assert the exact
	// surface. tags records the tags registered per name (review -> nil; the 16
	// internal reasoners -> ["review","pr"]).
	registered []string
	tags       map[string][]string
}

// RegisteredNames returns a copy of the reasoner names registered on this node,
// in registration order — the functional/unit parity source of truth (V1).
func (n *Node) RegisteredNames() []string {
	return append([]string(nil), n.registered...)
}

// TagsFor returns a copy of the tags registered for name (nil when none).
func (n *Node) TagsFor(name string) []string {
	return append([]string(nil), n.tags[name]...)
}

// defaultRunReview constructs the real orchestrator and runs it. Kept as a
// package function so BuildAgent can point Node.runReview at it and tests can
// swap in a stub.
func defaultRunReview(ctx context.Context, deps orch.Deps, in schemas.ReviewInput, cfg config.ReviewConfig) (schemas.ReviewResult, error) {
	return orch.New(deps, in, cfg).Run(ctx)
}

// BuildAgent constructs the PR-AF agent from the environment exactly as the
// Python entry point does (app.py:26-50):
//
//   - NODE_ID            default "pr-af-go" (opt-in sibling; distinct from the
//     Python "pr-af" node so both run against one control plane).
//   - AGENTFIELD_SERVER  default "http://localhost:8080".
//   - AGENTFIELD_API_KEY -> Config.Token (control-plane bearer).
//   - PORT               default "8007" -> ListenAddress ":8007".
//   - AGENT_CALLBACK_URL -> Config.PublicURL — the base URL the CP uses to reach
//     this node; unset falls back to the SDK's http://localhost:<port>.
//   - HarnessConfig / AIConfig — the harness (opencode) + LLM credentials the
//     reasoners rely on. Every reasoner calls the harness with only Cwd set, so
//     the agent's default HarnessConfig Provider/Model must be present, and the
//     two .ai() gates (intake/coverage) need AIConfig. Mirrors app.py's
//     harness_config=/ai_config=.
//
// Divergence from Python (documented, deliberate): the Go SDK's ai.Config
// rejects an empty API key at construction, whereas Python's AIConfig accepts
// os.getenv("OPENROUTER_API_KEY","") == "". So AIConfig is attached ONLY when
// OPENROUTER_API_KEY is set — construction succeeds without a key (matching
// Python), and the AI call fails at call time either way when the key is absent.
func BuildAgent(defaultNodeID, defaultPort, description string) (*Node, error) {
	nodeID := envOr("NODE_ID", defaultNodeID)
	server := envOr("AGENTFIELD_SERVER", "http://localhost:8080")
	token := os.Getenv("AGENTFIELD_API_KEY")
	port := envOr("PORT", defaultPort)

	aiConf, err := config.AIConfigFromEnv()
	if err != nil {
		// Python constructs AIIntegrationConfig at module import, so a malformed
		// numeric env var (e.g. PR_AF_MAX_TURNS=abc) crashes the node at boot.
		return nil, err
	}

	cfg := agent.Config{
		NodeID:        nodeID,
		Version:       "0.1.0",
		AgentFieldURL: server,
		Token:         token,
		ListenAddress: ":" + port,
		PublicURL:     os.Getenv("AGENT_CALLBACK_URL"),
		CLIConfig:     &agent.CLIConfig{AppDescription: description},
		HarnessConfig: &agent.HarnessConfig{
			Provider:       aiConf.Provider,
			Model:          aiConf.HarnessModel,
			MaxTurns:       aiConf.MaxTurns,
			PermissionMode: "auto",
			Env:            aiConf.ProviderEnv(),
			BinPath:        aiConf.OpencodeBin,
		},
	}
	if apiKey := os.Getenv("OPENROUTER_API_KEY"); apiKey != "" {
		// Python's .ai() path runs through LiteLLM, which CONSUMES a leading
		// "openrouter/" as its routing prefix before calling the OpenRouter API.
		// The Go SDK's ai client posts the model string verbatim to BaseURL, and
		// OpenRouter rejects "openrouter/moonshotai/..." as an invalid model ID —
		// so strip the routing prefix here to reach the same model Python does.
		// The HARNESS model keeps the prefix (opencode's config wants it; the
		// entrypoint derives its model key by stripping it there too).
		cfg.AIConfig = &ai.Config{
			Model:   aiModelForAPI(aiConf.AIModel),
			APIKey:  apiKey,
			BaseURL: "https://openrouter.ai/api/v1",
		}
	}

	app, err := agent.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("create agent %q: %w", nodeID, err)
	}

	n := &Node{
		App:              app,
		NodeID:           nodeID,
		AgentFieldServer: server,
		ListenAddress:    ":" + port,
		reviewApp:        app,
		gh:               github.NewClient(""), // reads GH_TOKEN internally (app.py GitHubClient())
		localCaller:      app,
		runReview:        defaultRunReview,
		tags:             map[string][]string{},
	}
	return n, nil
}

// Serve runs the custom HTTP server and registers with the control plane.
//
// It mounts /webhook/github on the PR-AF handler and delegates every other path
// (/health, /reasoners/, /execute, /discover, …) to the SDK's App.Handler(),
// mirroring Python's app.add_api_route("/webhook/github", …) grafted onto the
// SDK's own routes.
//
// Ordering (design §G): bind the listener BEFORE App.Initialize so the control
// plane's post-registration health check reaches a live server — the same
// startServer→Initialize order agent.Serve uses, reproduced here because the
// webhook route forces a bespoke mux instead of App.Serve.
func (n *Node) Serve(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/github", n.webhookGitHub)
	mux.Handle("/", n.App.Handler())

	ln, err := net.Listen("tcp", n.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen %s: %w", n.ListenAddress, err)
	}
	srv := &http.Server{Handler: mux}

	serveErr := make(chan error, 1)
	go func() {
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			serveErr <- serr
		}
	}()

	if err := n.App.Initialize(ctx); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return fmt.Errorf("initialize node %q: %w", n.NodeID, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
	case sig := <-sigCh:
		log.Printf("pr-af: received signal %s, shutting down", sig)
	case err := <-serveErr:
		return fmt.Errorf("webhook server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// envOr returns the value of key, or def when the env var is unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// aiModelForAPI converts the configured AI model into the model ID the
// OpenRouter API expects. Python's .ai() path runs through LiteLLM, which
// CONSUMES a leading "openrouter/" as its routing prefix before calling the
// OpenRouter API; the Go SDK's ai client posts the model string verbatim to
// BaseURL, where "openrouter/moonshotai/..." is an invalid model ID. Stripping
// the routing prefix reaches the same model Python does. The HARNESS model is
// untouched (opencode's config expects the prefixed form).
func aiModelForAPI(model string) string {
	return strings.TrimPrefix(model, "openrouter/")
}
