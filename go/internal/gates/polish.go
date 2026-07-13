package gates

// Parallel polish pass (port of polish.py) — rewrites each inline comment body
// to be concise and developer-focused right before posting to GitHub. One AI
// call per comment, fired in parallel. On any per-comment failure the original
// body is kept unchanged.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Agent-Field/agentfield/sdk/go/ai"

	"github.com/Agent-Field/pr-af/go/internal/prompts"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// polishOne ports polish._polish_one: rewrite a single comment body. Any AI
// error, or an empty rewritten body, falls back to the original body verbatim.
func polishOne(ctx context.Context, app AICaller, body string) string {
	out, err := app.AI(ctx, prompts.PolishUserPrompt(body), ai.WithSystem(prompts.PolishSystem))
	if err != nil {
		fmt.Printf("[PR-AF] Polish skipped: %T\n", err)
		return body
	}
	text := strings.TrimSpace(responseText(out))
	if text == "" {
		return body
	}
	return text
}

// PolishComments ports polish.polish_comments: rewrite each comment body in
// parallel, original order preserved. Returns a NEW slice; each comment's Body
// is replaced with the rewritten text while every other field (Path, Line,
// Side) is copied unchanged — path preservation is guaranteed structurally
// here, and callout/markdown preservation is enforced by the polish system
// prompt. Pure — the input slice and its elements are not mutated.
//
// Concurrency mirrors classify_findings: Python's asyncio.gather never sees an
// exception because _polish_one catches its own AI errors, so a WaitGroup over
// a pre-indexed results slice is the faithful order-preserving fan-out.
func PolishComments(ctx context.Context, app AICaller, comments []schemas.GitHubComment) []schemas.GitHubComment {
	if len(comments) == 0 {
		return comments
	}
	newBodies := make([]string, len(comments))
	var wg sync.WaitGroup
	for i := range comments {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			newBodies[i] = polishOne(ctx, app, comments[i].Body)
		}(i)
	}
	wg.Wait()

	polished := make([]schemas.GitHubComment, len(comments))
	changed := 0
	for i, c := range comments {
		nc := c // shallow struct copy — replaces only Body.
		nc.Body = newBodies[i]
		polished[i] = nc
		if newBodies[i] != c.Body {
			changed++
		}
	}
	fmt.Printf("[PR-AF] Polish complete: %d/%d comments rewritten\n", changed, len(comments))
	return polished
}
