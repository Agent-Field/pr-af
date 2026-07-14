package orch

// DAG-routing parity: when Deps.Local is wired (production), every phase seam
// must invoke the SDK's tracked CallLocal under its registered reasoner name —
// that is what makes each phase a child execution on the control plane and
// renders the same pipeline DAG the Python port produces. Contract items:
//
//  1. each seam routes through CallLocal with the exact registered name;
//  2. the input map CallLocal receives afx.Binds back into the identical typed
//     input the orchestrator built (lossless round trip — including deliberate
//     zero values on fields whose Bind-side defaults are non-zero);
//  3. the handler's map[string]any result passes through unchanged;
//  4. a CallLocal error propagates unchanged;
//  5. Deps.Local == nil keeps the plain direct-call seams (stub-based tests
//     and the pre-DAG behavior).

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Agent-Field/pr-af/go/internal/afx"
	"github.com/Agent-Field/pr-af/go/internal/config"
	"github.com/Agent-Field/pr-af/go/internal/reasoners"
	"github.com/Agent-Field/pr-af/go/internal/schemas"
)

// fakeLocal records the last CallLocal invocation and returns a scripted
// result/error.
type fakeLocal struct {
	names  []string
	name   string
	input  map[string]any
	result any
	err    error
}

func (f *fakeLocal) CallLocal(_ context.Context, name string, input map[string]any) (any, error) {
	f.names = append(f.names, name)
	f.name = name
	f.input = input
	return f.result, f.err
}

// checkSeam drives one CallLocal-routed seam and asserts contract items 1-3.
func checkSeam[T any](
	t *testing.T,
	fake *fakeLocal,
	seam func(context.Context, reasoners.Deps, T) (map[string]any, error),
	wantName string,
	in T,
) {
	t.Helper()
	want := map[string]any{"ok": true, "phase": wantName}
	fake.result, fake.err = want, nil

	got, err := seam(context.Background(), reasoners.Deps{}, in)
	if err != nil {
		t.Fatalf("%s: seam error: %v", wantName, err)
	}
	if fake.name != wantName {
		t.Fatalf("seam called CallLocal(%q), want %q", fake.name, wantName)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: result = %v, want the handler map unchanged", wantName, got)
	}

	back, err := afx.Bind[T](fake.input)
	if err != nil {
		t.Fatalf("%s: input map does not Bind back: %v", wantName, err)
	}
	if !reflect.DeepEqual(back, in) {
		t.Errorf("%s: round trip mutated the input:\n sent: %#v\n back: %#v", wantName, in, back)
	}
}

func TestCallLocalSeamsRouteEveryPhase(t *testing.T) {
	fake := &fakeLocal{}
	o := New(Deps{App: &fakeApp{}, Local: fake}, schemas.ReviewInput{}, config.DefaultReviewConfig())

	intake := schemas.IntakeResult{PrSummary: "s", Complexity: "standard"}
	anatomy := schemas.AnatomyResult{}
	patches := reasoners.OrderedPatches{{Key: "b.go", Val: "@@ -1 +1 @@"}, {Key: "a.go", Val: "@@ -2 +2 @@"}}

	checkSeam(t, fake, o.rfns.intake, reasoners.NameIntakePhase,
		reasoners.IntakeInput{PRData: schemas.GitHubPRData{Title: "t"}, Depth: "deep"})
	checkSeam(t, fake, o.rfns.anatomy, reasoners.NameAnatomyPhase,
		reasoners.AnatomyInput{Intake: intake, RepoPath: "/repo"})
	checkSeam(t, fake, o.rfns.metaSemantic, reasoners.NameMetaSemantic,
		reasoners.MetaInput{Intake: intake, Anatomy: anatomy, Depth: "standard", DiffPatches: patches})
	checkSeam(t, fake, o.rfns.metaMechanical, reasoners.NameMetaMechanical,
		reasoners.MetaInput{Depth: "quick"})
	checkSeam(t, fake, o.rfns.metaSystemic, reasoners.NameMetaSystemic,
		reasoners.MetaInput{ReviewerFeedback: "fb"})
	// CurrentDepth=1 with a deliberate MaxDepth=0: Bind seeds MaxDepth=2 only
	// for an ABSENT key, and ToMap always emits the key — a zero the
	// orchestrator sets must survive the round trip (contract item 2).
	checkSeam(t, fake, o.rfns.reviewDim, reasoners.NameReviewDimension,
		reasoners.ReviewDimensionInput{
			ReviewPrompt: "look", TargetFiles: []string{"a.go"}, CurrentDepth: 1, MaxDepth: 0,
			DiffPatches: map[string]string{"a.go": "@@"},
		})
	checkSeam(t, fake, o.rfns.postWorthiness, reasoners.NamePostWorthinessGate,
		reasoners.PostWorthinessInput{Findings: []schemas.ReviewFinding{}})
	checkSeam(t, fake, o.rfns.evidenceVerify, reasoners.NameEvidenceVerifier,
		reasoners.EvidenceVerifierInput{PrContext: "ctx"})
	checkSeam(t, fake, o.rfns.adversary, reasoners.NameAdversaryPhase,
		reasoners.AdversaryInput{AIGeneratedConfidence: 0.7})
	checkSeam(t, fake, o.rfns.compoundFinder, reasoners.NameCompoundFinderPhase,
		reasoners.CompoundFinderInput{RepoPath: "/repo"})
	checkSeam(t, fake, o.rfns.compoundDedup, reasoners.NameCompoundDedupPhase,
		reasoners.CompoundDedupInput{IndividualFindingsSummary: "sum"})
	checkSeam(t, fake, o.rfns.coverageGate, reasoners.NameCoverageGate,
		reasoners.CoverageGateInput{})
	checkSeam(t, fake, o.rfns.extractOblig, reasoners.NameExtractObligations,
		reasoners.ExtractObligationsInput{DiffPatches: patches})
	checkSeam(t, fake, o.rfns.verifyOblig, reasoners.NameVerifyObligation,
		reasoners.VerifyObligationInput{RepoPath: "/repo"})

	// Every invoked name must be one the node registers on the control plane —
	// the DAG's phase nodes carry these exact reasoner ids.
	wantNames := []string{
		reasoners.NameIntakePhase, reasoners.NameAnatomyPhase,
		reasoners.NameMetaSemantic, reasoners.NameMetaMechanical, reasoners.NameMetaSystemic,
		reasoners.NameReviewDimension, reasoners.NamePostWorthinessGate,
		reasoners.NameEvidenceVerifier, reasoners.NameAdversaryPhase,
		reasoners.NameCompoundFinderPhase, reasoners.NameCompoundDedupPhase,
		reasoners.NameCoverageGate, reasoners.NameExtractObligations, reasoners.NameVerifyObligation,
	}
	if !reflect.DeepEqual(fake.names, wantNames) {
		t.Errorf("CallLocal names = %v, want %v", fake.names, wantNames)
	}
}

func TestCallLocalSeamPropagatesError(t *testing.T) {
	fake := &fakeLocal{err: errors.New("harness exploded")}
	o := New(Deps{App: &fakeApp{}, Local: fake}, schemas.ReviewInput{}, config.DefaultReviewConfig())

	_, err := o.rfns.intake(context.Background(), reasoners.Deps{}, reasoners.IntakeInput{})
	if err == nil || err.Error() != "harness exploded" {
		t.Fatalf("err = %v, want the CallLocal error unchanged", err)
	}
}

func TestCallLocalSeamRejectsNonMapResult(t *testing.T) {
	fake := &fakeLocal{result: "not a map"}
	o := New(Deps{App: &fakeApp{}, Local: fake}, schemas.ReviewInput{}, config.DefaultReviewConfig())

	_, err := o.rfns.intake(context.Background(), reasoners.Deps{}, reasoners.IntakeInput{})
	if err == nil {
		t.Fatal("expected an error for a non-map CallLocal result")
	}
}

func TestCallLocalSeamNilResultYieldsEmptyMap(t *testing.T) {
	fake := &fakeLocal{}
	o := New(Deps{App: &fakeApp{}, Local: fake}, schemas.ReviewInput{}, config.DefaultReviewConfig())

	got, err := o.rfns.intake(context.Background(), reasoners.Deps{}, reasoners.IntakeInput{})
	if err != nil {
		t.Fatalf("seam error: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %v, want an empty non-nil map", got)
	}
}

func TestNilLocalKeepsDirectSeams(t *testing.T) {
	o := New(Deps{App: &fakeApp{}}, schemas.ReviewInput{}, config.DefaultReviewConfig())

	if reflect.ValueOf(o.rfns.intake).Pointer() != reflect.ValueOf(reasoners.IntakePhase).Pointer() {
		t.Error("intake seam is not the direct reasoners.IntakePhase when Local is nil")
	}
	if reflect.ValueOf(o.rfns.verifyOblig).Pointer() != reflect.ValueOf(reasoners.VerifyObligation).Pointer() {
		t.Error("verifyOblig seam is not the direct reasoners.VerifyObligation when Local is nil")
	}
}
