// Command pr-af is the Go PR-AF review node (the port of src/pr_af/app.py's
// main()). It builds the agent from the environment, registers the 17-reasoner
// surface (design §B.1), and serves the SDK handler plus the custom
// /webhook/github route until SIGINT/SIGTERM.
//
// Defaults: NODE_ID "pr-af", PORT 8007. NODE_ID / PORT env vars override;
// docker-compose.go.yml does so when both implementations coexist.
//
// Boot env (T4.3 e2e / production):
//
//	AGENTFIELD_SERVER   control-plane base URL (default http://localhost:8080)
//	AGENTFIELD_API_KEY  control-plane bearer token
//	AGENT_CALLBACK_URL  base URL the CP uses to reach this node (else localhost)
//	NODE_ID             node id (default pr-af)
//	PORT                listen port (default 8007)
//	PR_AF_PROVIDER      harness provider (default aforge; accepts opencode rollback)
//	PR_AF_MODEL         harness model (env wins over the code default)
//	PR_AF_HARNESS_BIN   optional executable override for every harness provider
//	OPENROUTER_API_KEY  LLM key — required for the .ai() gates; AIConfig is only
//	                    attached when set (SDK rejects an empty key)
//	GH_TOKEN            GitHub token for FetchPR/clone/PostReview (optional —
//	                    public repos review anonymously; needed for private
//	                    repos and posting reviews)
//	GITHUB_WEBHOOK_SECRET  HMAC secret for /webhook/github (skip verify if unset)
//	PR_AF_BOT_MENTION   webhook trigger mention (default @pr-af)
package main

import (
	"context"
	"log"

	"github.com/Agent-Field/pr-af/go/internal/node"
)

func main() {
	n, err := node.BuildAgent(
		"pr-af",
		"8007",
		"AI-Native Pull Request Review Agent",
	)
	if err != nil {
		log.Fatalf("pr-af: build agent: %v", err)
	}

	n.RegisterAll()

	if err := n.Serve(context.Background()); err != nil {
		log.Fatalf("pr-af: serve: %v", err)
	}
}
