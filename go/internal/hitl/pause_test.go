package hitl

import (
	"context"
	"reflect"
	"testing"

	"github.com/Agent-Field/agentfield/sdk/go/agent"
)

// fakePauser is a test double for the Pauser seam. Its existence + the
// assignment below prove a non-agent type can satisfy the interface, which is
// what lets the review gate be unit-tested without a live control plane.
type fakePauser struct {
	gotOpts agent.PauseOptions
	result  *agent.ApprovalResult
	err     error
}

func (f *fakePauser) Pause(_ context.Context, opts agent.PauseOptions) (*agent.ApprovalResult, error) {
	f.gotOpts = opts
	return f.result, f.err
}

var _ Pauser = (*fakePauser)(nil)

// TestPauserSeam confirms a fake satisfies the Pauser contract and round-trips
// options and result — the seam the review gate (T3.3) drives.
func TestPauserSeam(t *testing.T) {
	want := &agent.ApprovalResult{Decision: "approved", Feedback: "lgtm"}
	f := &fakePauser{result: want}
	var p Pauser = f

	got, err := p.Pause(context.Background(), agent.PauseOptions{ApprovalRequestID: "req_1", ExpiresInHours: 72})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
	if f.gotOpts.ApprovalRequestID != "req_1" || f.gotOpts.ExpiresInHours != 72 {
		t.Errorf("opts not forwarded: %+v", f.gotOpts)
	}
}

func TestExtractValuesFromRaw(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want map[string]any
	}{
		{name: "nil -> empty", raw: nil, want: map[string]any{}},
		{
			name: "direct values",
			raw:  map[string]any{"values": map[string]any{"action": "post_selected"}},
			want: map[string]any{"action": "post_selected"},
		},
		{
			name: "nested response.values",
			raw:  map[string]any{"response": map[string]any{"values": map[string]any{"action": "rerun"}}},
			want: map[string]any{"action": "rerun"},
		},
		{
			name: "direct wins over nested",
			raw: map[string]any{
				"values":   map[string]any{"action": "reject"},
				"response": map[string]any{"values": map[string]any{"action": "rerun"}},
			},
			want: map[string]any{"action": "reject"},
		},
		{name: "values wrong type -> empty", raw: map[string]any{"values": "not-a-map"}, want: map[string]any{}},
		{name: "no values anywhere -> empty", raw: map[string]any{"other": 1}, want: map[string]any{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractValuesFromRaw(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractValuesFromRaw(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
