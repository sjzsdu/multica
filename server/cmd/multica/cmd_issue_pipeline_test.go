package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// `multica issue verdict` is what the product agent calls every round, and the
// squad workflow flags are what configure the cap it counts against. Both are
// pure input handling here: the endpoint itself is covered in
// server/internal/handler/issue_pipeline_test.go.

func newIssueVerdictTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "verdict"}
	c.Flags().String("reason", "", "")
	c.Flags().Int("round", 0, "")
	c.Flags().String("output", "table", "")
	return c
}

func newIssueReviewTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "review"}
	c.Flags().String("run", "", "")
	c.Flags().Int("round", 0, "")
	c.Flags().String("reason", "", "")
	c.Flags().String("output", "json", "")
	return c
}

func TestRunIssueVerdictValidatesInput(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		reason  string
		wantErr string
	}{
		{name: "unknown verdict", args: []string{"MUL-1", "maybe"}, wantErr: "verdict must be"},
		{name: "accept is case-insensitive but spelled", args: []string{"MUL-1", "ACCEPTED"}, wantErr: "verdict must be"},
		{name: "reject needs a reason", args: []string{"MUL-1", "reject"}, wantErr: "--reason is required"},
		{name: "whitespace reason is no reason", args: []string{"MUL-1", "reject"}, reason: "   ", wantErr: "--reason is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newIssueVerdictTestCmd()
			if err := cmd.Flags().Set("reason", tt.reason); err != nil {
				t.Fatalf("set --reason: %v", err)
			}
			err := runIssueVerdict(cmd, tt.args)
			if err == nil {
				t.Fatal("error = nil, want a validation error before any request is sent")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func pipelineCommandTestServer(t *testing.T, wantPath string, response map[string]any) *map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	return &body
}

func TestRunIssueVerdictSendsOptionalRound(t *testing.T) {
	for _, tt := range []struct {
		name      string
		setRound  bool
		wantRound any
	}{
		{name: "legacy omission", wantRound: nil},
		{name: "current round", setRound: true, wantRound: float64(2)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := pipelineCommandTestServer(t, "/api/issues/"+testIssueUUID+"/pipeline/verdict", map[string]any{
				"verdict": "accept", "round": 0, "max_rounds": 3, "escalated": false,
			})
			cmd := newIssueVerdictTestCmd()
			_ = cmd.Flags().Set("output", "json")
			if tt.setRound {
				_ = cmd.Flags().Set("round", "2")
			}
			if _, err := captureStdout(t, func() error {
				return runIssueVerdict(cmd, []string{testIssueUUID, "accept"})
			}); err != nil {
				t.Fatalf("runIssueVerdict: %v", err)
			}
			if got := (*body)["round"]; got != tt.wantRound {
				t.Fatalf("round = %#v, want %#v (body: %#v)", got, tt.wantRound, *body)
			}
		})
	}
}

func TestRunIssueVerdictRejectsNegativeRound(t *testing.T) {
	cmd := newIssueVerdictTestCmd()
	_ = cmd.Flags().Set("round", "-1")
	if err := runIssueVerdict(cmd, []string{testIssueUUID, "accept"}); err == nil || !strings.Contains(err.Error(), "--round must be >= 0") {
		t.Fatalf("error = %v, want nonnegative round validation", err)
	}
}

func TestRunIssueReviewValidatesInput(t *testing.T) {
	tests := []struct {
		name     string
		action   string
		run      string
		setRound bool
		round    string
		reason   string
		wantErr  string
	}{
		{name: "unknown action", action: "maybe", wantErr: "review decision must be"},
		{name: "run required", action: "approve", setRound: true, round: "0", wantErr: "--run is required"},
		{name: "round required", action: "approve", run: "run-1", wantErr: "--round is required"},
		{name: "nonnegative round", action: "approve", run: "run-1", setRound: true, round: "-1", wantErr: "--round must be >= 0"},
		{name: "rejection reason", action: "reject", run: "run-1", setRound: true, round: "0", wantErr: "--reason is required"},
		{name: "whitespace rejection reason", action: "reject", run: "run-1", setRound: true, round: "0", reason: "  ", wantErr: "--reason is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newIssueReviewTestCmd()
			_ = cmd.Flags().Set("run", tt.run)
			_ = cmd.Flags().Set("reason", tt.reason)
			if tt.setRound {
				_ = cmd.Flags().Set("round", tt.round)
			}
			err := runIssueReview(cmd, []string{testIssueUUID, tt.action})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunIssueReviewSendsCurrentRoundDecision(t *testing.T) {
	const runID = "33333333-3333-3333-3333-333333333333"
	for _, tt := range []struct {
		action   string
		approved bool
		reason   string
	}{
		{action: "approve", approved: true},
		{action: "reject", approved: false, reason: "regression in checkout"},
	} {
		t.Run(tt.action, func(t *testing.T) {
			body := pipelineCommandTestServer(t, "/api/issues/"+testIssueUUID+"/workflow-runs/"+runID+"/review", map[string]any{
				"id": "approval-1", "approved": tt.approved, "pipeline_round": 2,
			})
			cmd := newIssueReviewTestCmd()
			_ = cmd.Flags().Set("run", runID)
			_ = cmd.Flags().Set("round", "2")
			_ = cmd.Flags().Set("reason", tt.reason)
			if _, err := captureStdout(t, func() error {
				return runIssueReview(cmd, []string{testIssueUUID, tt.action})
			}); err != nil {
				t.Fatalf("runIssueReview: %v", err)
			}
			want := map[string]any{"approved": tt.approved, "note": tt.reason, "round": float64(2)}
			if !reflect.DeepEqual(*body, want) {
				t.Fatalf("request body = %#v, want %#v", *body, want)
			}
		})
	}
}

func newWorkflowPatchTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "squad-update"}
	addSquadWorkflowFlags(c)
	return c
}

// TestWorkflowPatchFromFlagsPipelineBlock pins the whole-block replacement the
// server performs: only an explicitly supplied flag set reaches the API, and
// max rounds 0 is how a squad turns the pipeline back off.
func TestWorkflowPatchFromFlagsPipelineBlock(t *testing.T) {
	const productUUID = "22222222-2222-2222-2222-222222222222"
	tests := []struct {
		name      string
		flags     map[string]string
		wantSet   bool
		wantValue any
		wantErr   string
	}{
		{name: "no pipeline flags leaves the stored block alone"},
		{
			name:      "max rounds alone drops the product agent",
			flags:     map[string]string{"pipeline-max-rounds": "3"},
			wantSet:   true,
			wantValue: map[string]any{"max_rounds": 3},
		},
		{
			name:    "max rounds with a product agent",
			flags:   map[string]string{"pipeline-max-rounds": "2", "pipeline-product-agent": productUUID},
			wantSet: true,
			wantValue: map[string]any{
				"max_rounds":       2,
				"product_agent_id": productUUID,
			},
		},
		{
			name:      "max rounds 0 clears the block",
			flags:     map[string]string{"pipeline-max-rounds": "0"},
			wantSet:   true,
			wantValue: nil,
		},
		{
			name:    "product agent without max rounds",
			flags:   map[string]string{"pipeline-product-agent": productUUID},
			wantErr: "--pipeline-max-rounds is required",
		},
		{
			name:    "product agent with the pipeline off",
			flags:   map[string]string{"pipeline-max-rounds": "0", "pipeline-product-agent": productUUID},
			wantErr: "--pipeline-product-agent requires",
		},
		{
			name:    "negative max rounds",
			flags:   map[string]string{"pipeline-max-rounds": "-1"},
			wantErr: "--pipeline-max-rounds must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newWorkflowPatchTestCmd()
			for name, value := range tt.flags {
				if err := cmd.Flags().Set(name, value); err != nil {
					t.Fatalf("set --%s: %v", name, err)
				}
			}

			// A UUID product agent resolves without the API, so no client is
			// needed to exercise the patch builder.
			patch, err := workflowPatchFromFlags(context.Background(), nil, cmd)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("error = nil, want it to contain %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("workflowPatchFromFlags: %v", err)
			}

			got, set := patch["pipeline"]
			if set != tt.wantSet {
				t.Fatalf("pipeline key present = %v, want %v (patch: %#v)", set, tt.wantSet, patch)
			}
			if !set {
				return
			}
			if tt.wantValue == nil {
				if got != nil {
					t.Fatalf("pipeline = %#v, want nil to clear the block", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.wantValue) {
				t.Fatalf("pipeline = %#v, want %#v", got, tt.wantValue)
			}
		})
	}
}
