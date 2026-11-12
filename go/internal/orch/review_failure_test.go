package orch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
	"github.com/Agent-Field/agentfield/sdk/go/ai"
	"github.com/Agent-Field/agentfield/sdk/go/harness"
	"github.com/Agent-Field/pr-af/go/internal/afx"
	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/hitl"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
	"net/http"
	"net/http/httptest"
)

// Exercise the real phase implementations, including their deterministic
// fallbacks. The only replaced capabilities are GitHub and model execution.
type reviewFixtureApp struct {
	fakeApp
	fail string
}

func (f *reviewFixtureApp) AI(context.Context, string, ...ai.Option) (*ai.Response, error) {
	return nil, errors.New("AI unavailable in offline fixture")
}

func (f *reviewFixtureApp) Harness(_ context.Context, prompt string, _ map[string]any, dest any, _ harness.Options) (*harness.Result, error) {
	kind := reflect.TypeOf(dest).Elem().Name()
	failed := f.fail == "all" || f.fail == kind ||
		(f.fail == "meta_mechanical" && kind == "MetaDimensionResult" && strings.Contains(prompt, "MECHANICAL")) ||
		(f.fail == "missing reviewer" && kind == "reviewFindingsResult" && strings.Contains(prompt, "fail dimension"))
	if failed {
		return &harness.Result{IsError: true, ErrorMessage: "model configuration unavailable"}, nil
	}
	payload := `{}`
	switch kind {
	case "MetaDimensionResult":
		payload = `{"dimensions":[{"id":"ok","name":"completed reviewer","review_prompt":"complete dimension","target_files":["a.go"]},{"id":"bad","name":"missing reviewer","review_prompt":"fail dimension","target_files":["b.go"]}],"confidence":0.9}`
		if f.fail == "empty plan" {
			payload = `{"dimensions":[],"confidence":0.9}`
		}
	case "obligationsResult":
		if f.fail == "obligationVerdict" {
			payload = `{"obligations":[{"id":"one","where":"a.go:1","relies_on":"b.go:1","property":"consistent"}]}`
		}
	case "CoverageGate":
		payload = `{"fully_covered":true,"confident":true,"gap_descriptions":[]}`
	}
	if err := json.Unmarshal([]byte(payload), dest); err != nil {
		return nil, err
	}
	return &harness.Result{Parsed: dest, Result: payload}, nil
}

type reviewFixtureGH struct{ reviews []schemas.GitHubReview }

func (*reviewFixtureGH) ParsePRURL(string) (string, string, int, error) { return "o", "r", 1, nil }
func (*reviewFixtureGH) FetchPR(context.Context, string, string, int) (schemas.GitHubPRData, error) {
	return schemas.GitHubPRData{Owner: "o", Repo: "r", Number: 1, Title: "fixture", ChangedFiles: []schemas.ChangedFile{{Path: "a.go", Patch: "@@ -1 +1 @@\n-old\n+new"}}}, nil
}
func (g *reviewFixtureGH) PostReview(_ context.Context, _ string, _ string, _ int, r schemas.GitHubReview, _ string) (map[string]any, error) {
	g.reviews = append(g.reviews, r)
	return map[string]any{"id": 1}, nil
}

func fixtureRegistration[T any](app *agent.Agent, deps reasoners.Deps, name string, fn func(context.Context, reasoners.Deps, T) (map[string]any, error)) {
	app.RegisterReasoner(name, func(ctx context.Context, input map[string]any) (any, error) {
		in, err := afx.Bind[T](input)
		if err != nil {
			return nil, err
		}
		return fn(ctx, deps, in)
	})
}

func TestReviewNeverPublishesCleanAfterHarnessFailure(t *testing.T) {
	for _, tracked := range []bool{false, true} {

		for _, tc := range []struct {
			fail, missing string
			wantError     bool
		}{
			{"all", "", true},
			{"MetaDimensionResult", "", true},
			{"empty plan", "", true},
			{"IntakeResult", "intake_phase", false},
			{"anatomySemanticResult", "anatomy_phase", false},
			{"meta_mechanical", "meta_mechanical", false},
			{"CoverageGate", "coverage_gate", false},
			{"obligationsResult", "extract_obligations", false},
			{"missing reviewer", "missing reviewer", false},
			{"obligationVerdict", "verify_obligation", false},
			{"", "", false},
		} {
			t.Run(fmt.Sprintf("tracked=%t/%s", tracked, tc.fail), func(t *testing.T) {
				gh := &reviewFixtureGH{}
				cfg := config.DefaultReviewConfig()
				cfg.Comments.PolishEnabled = false
				cfg.Comments.MergeGateEnabled = false
				deps := Deps{App: &reviewFixtureApp{fail: tc.fail}, GH: gh}
				if tracked {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) }))
					defer server.Close()
					app, err := agent.New(agent.Config{NodeID: "fixture", Version: "1.0.0", AgentFieldURL: server.URL})
					if err != nil {
						t.Fatal(err)
					}
					rd := reasoners.Deps{Harness: deps.App, AI: deps.App}
					fixtureRegistration(app, rd, reasoners.NameIntakePhase, reasoners.IntakePhase)
					fixtureRegistration(app, rd, reasoners.NameAnatomyPhase, reasoners.AnatomyPhase)
					fixtureRegistration(app, rd, reasoners.NameMetaSemantic, reasoners.MetaSemantic)
					fixtureRegistration(app, rd, reasoners.NameMetaMechanical, reasoners.MetaMechanical)
					fixtureRegistration(app, rd, reasoners.NameMetaSystemic, reasoners.MetaSystemic)
					fixtureRegistration(app, rd, reasoners.NameReviewDimension, reasoners.ReviewDimension)
					fixtureRegistration(app, rd, reasoners.NameCoverageGate, reasoners.CoverageGate)
					fixtureRegistration(app, rd, reasoners.NameExtractObligations, reasoners.ExtractObligations)
					fixtureRegistration(app, rd, reasoners.NameVerifyObligation, reasoners.VerifyObligation)
					deps.Local = app
				}
				o := New(deps, schemas.ReviewInput{PrURL: strPtr("https://github.com/o/r/pull/1"), Depth: "standard"}, cfg)
				o.buildHaxClientFn = func() *hitl.HaxClient { return nil }
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, err := o.Run(ctx)
				if tc.wantError {
					if err == nil || len(gh.reviews) != 0 {
						t.Fatalf("unreviewed PR: err=%v, publication attempts=%d, body=%s", err, len(gh.reviews), result.Review.Body)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				body := result.Review.Body
				if tc.missing == "" {
					if !strings.Contains(body, "Looks Good") {
						t.Fatalf("successful review lost clean wording: %s", body)
					}
					return
				}
				if !strings.Contains(body, "Review Incomplete") || !strings.Contains(body, tc.missing) || strings.Contains(body, "Safe to") || strings.Contains(body, "Safe to merge") || strings.Contains(body, "Looks Good") {
					t.Fatalf("missing honest failure verdict naming %q: %s", tc.missing, body)
				}
				b, _ := json.Marshal(result.Metadata)
				if !strings.Contains(string(b), tc.missing) {
					t.Fatalf("missing work absent from metadata: %s", b)
				}
				for _, review := range gh.reviews {
					if review.Event == "APPROVE" || !strings.Contains(review.Body, "Review Incomplete") {
						t.Fatalf("published clean verdict: %+v", review)
					}
				}
			})
		}
	}

}
