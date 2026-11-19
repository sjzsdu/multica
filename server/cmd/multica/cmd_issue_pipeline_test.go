package main

import (
	"context"
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
	c.Flags().String("output", "table", "")
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
			name:  "max rounds with a product agent",
			flags: map[string]string{"pipeline-max-rounds": "2", "pipeline-product-agent": productUUID},
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
