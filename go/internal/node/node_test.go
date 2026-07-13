package node

import (
	"testing"
)

// TestBuildAgentFromEnv is the main.go smoke: BuildAgent resolves node identity
// from the environment (with the pr-af-go / 8007 defaults), constructs the agent
// without a control plane or LLM key, and RegisterAll wires the full surface.
func TestBuildAgentFromEnv(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		wantNodeID string
		wantServer string
		wantListen string
	}{
		{
			name:       "defaults when env unset",
			env:        map[string]string{"NODE_ID": "", "PORT": "", "AGENTFIELD_SERVER": "", "OPENROUTER_API_KEY": ""},
			wantNodeID: "pr-af-go",
			wantServer: "http://localhost:8080",
			wantListen: ":8007",
		},
		{
			name: "env overrides",
			env: map[string]string{
				"NODE_ID":            "pr-af-go-canary",
				"PORT":               "9107",
				"AGENTFIELD_SERVER":  "http://cp.internal:8080",
				"OPENROUTER_API_KEY": "", // keep AIConfig off so New needs no key
			},
			wantNodeID: "pr-af-go-canary",
			wantServer: "http://cp.internal:8080",
			wantListen: ":9107",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			n, err := BuildAgent("pr-af-go", "8007", "AI-Native Pull Request Review Agent")
			if err != nil {
				t.Fatalf("BuildAgent: %v", err)
			}
			if n.App == nil {
				t.Fatal("BuildAgent returned a nil App")
			}
			if n.NodeID != tc.wantNodeID {
				t.Errorf("NodeID = %q, want %q", n.NodeID, tc.wantNodeID)
			}
			if n.AgentFieldServer != tc.wantServer {
				t.Errorf("AgentFieldServer = %q, want %q", n.AgentFieldServer, tc.wantServer)
			}
			if n.ListenAddress != tc.wantListen {
				t.Errorf("ListenAddress = %q, want %q", n.ListenAddress, tc.wantListen)
			}

			n.RegisterAll()
			if got := len(n.RegisteredNames()); got != 17 {
				t.Errorf("registered %d reasoners, want 17", got)
			}
		})
	}
}

// TestBuildAgentWithLLMKey proves AIConfig attaches (and agent.New still
// succeeds) when OPENROUTER_API_KEY is present — the production path.
func TestBuildAgentWithLLMKey(t *testing.T) {
	t.Setenv("NODE_ID", "")
	t.Setenv("PORT", "")
	t.Setenv("AGENTFIELD_SERVER", "")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	n, err := BuildAgent("pr-af-go", "8007", "desc")
	if err != nil {
		t.Fatalf("BuildAgent with LLM key: %v", err)
	}
	if n.App == nil {
		t.Fatal("nil App")
	}
}

// The .ai() path must receive the OpenRouter API model ID, with LiteLLM's
// "openrouter/" routing prefix stripped — Python consumes that prefix in
// LiteLLM, so a prefixed PR_AF_MODEL (the deploy default) must not reach the
// OpenRouter API verbatim. Unprefixed models pass through untouched.
func TestAIModelForAPIStripsOpenRouterRoutingPrefix(t *testing.T) {
	if got := aiModelForAPI("openrouter/moonshotai/kimi-k2.5"); got != "moonshotai/kimi-k2.5" {
		t.Errorf("prefixed: got %q, want moonshotai/kimi-k2.5", got)
	}
	if got := aiModelForAPI("minimax/minimax-m2.5"); got != "minimax/minimax-m2.5" {
		t.Errorf("unprefixed: got %q, want unchanged", got)
	}
}
