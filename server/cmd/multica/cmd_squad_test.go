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

	"github.com/multica-ai/multica/server/internal/cli"
)

func newSquadMemberSetRoleTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "set-role"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("member-id", "", "")
	cmd.Flags().String("member-type", "agent", "")
	cmd.Flags().String("role", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunSquadMemberSetRolePatchesRole(t *testing.T) {
	redirectTestHome(t, t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if r.Header.Get("X-Workspace-ID") != "workspace-123" {
			t.Fatalf("X-Workspace-ID = %q, want workspace-123", r.Header.Get("X-Workspace-ID"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"squad_id":    "squad-123",
			"member_id":   "member-456",
			"member_type": "agent",
			"role":        "reviewer",
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newSquadMemberSetRoleTestCmd()
	_ = cmd.Flags().Set("member-id", "member-456")
	_ = cmd.Flags().Set("member-type", "agent")
	_ = cmd.Flags().Set("role", "reviewer")
	_ = cmd.Flags().Set("output", "json")

	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err != nil {
		t.Fatalf("runSquadMemberSetRole: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/api/squads/squad-123/members/role" {
		t.Fatalf("path = %q, want /api/squads/squad-123/members/role", gotPath)
	}
	wantBody := map[string]any{"member_id": "member-456", "member_type": "agent", "role": "reviewer"}
	for k, want := range wantBody {
		if gotBody[k] != want {
			t.Fatalf("body[%s] = %v, want %v (full body: %#v)", k, gotBody[k], want, gotBody)
		}
	}
}

func TestRunSquadMemberSetRoleValidatesRequiredFlags(t *testing.T) {
	cmd := newSquadMemberSetRoleTestCmd()
	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err == nil {
		t.Fatal("expected missing --member-id error")
	}

	cmd = newSquadMemberSetRoleTestCmd()
	_ = cmd.Flags().Set("member-id", "member-456")
	_ = cmd.Flags().Set("member-type", "invalid")
	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err == nil {
		t.Fatal("expected invalid --member-type error")
	}

	cmd = newSquadMemberSetRoleTestCmd()
	_ = cmd.Flags().Set("member-id", "member-456")
	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err == nil {
		t.Fatal("expected missing --role error")
	}
}

func newSquadWorkflowPatchTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "squad-update"}
	addSquadWorkflowFlags(cmd)
	return cmd
}

func TestWorkflowPatchFromFlagsBlackboxOptIns(t *testing.T) {
	const agentUUID = "33333333-3333-3333-3333-333333333333"
	tests := []struct {
		name    string
		flags   map[string]string
		want    map[string]any
		wantErr string
	}{
		{
			name: "omitted flags leave stored policy unchanged",
			want: map[string]any{},
		},
		{
			name: "enabled flags use workflow wire keys without requiring a pipeline replacement",
			flags: map[string]string{
				"final-gates-only":         "true",
				"review-agent":             agentUUID,
				"require-pipeline-verdict": "true",
			},
			want: map[string]any{
				"final_gates_only":         true,
				"review_agent_id":          agentUUID,
				"require_pipeline_verdict": true,
			},
		},
		{
			name: "explicit false and empty values clear stored opt ins",
			flags: map[string]string{
				"final-gates-only":         "false",
				"review-agent":             "",
				"require-pipeline-verdict": "false",
			},
			want: map[string]any{
				"final_gates_only":         false,
				"review_agent_id":          "",
				"require_pipeline_verdict": false,
			},
		},
		{
			name: "review agent rejects explicitly disabled review gate",
			flags: map[string]string{
				"review-agent":   agentUUID,
				"require-review": "false",
			},
			wantErr: "--review-agent requires --require-review",
		},
		{
			name: "pipeline verdict rejects a cleared replacement pipeline",
			flags: map[string]string{
				"require-pipeline-verdict": "true",
				"pipeline-max-rounds":      "0",
			},
			wantErr: "--require-pipeline-verdict requires --pipeline-product-agent",
		},
		{
			name: "pipeline verdict rejects a replacement pipeline without product agent",
			flags: map[string]string{
				"require-pipeline-verdict": "true",
				"pipeline-max-rounds":      "3",
			},
			wantErr: "--require-pipeline-verdict requires --pipeline-product-agent",
		},
		{
			name: "reviewer and product agent must differ",
			flags: map[string]string{
				"review-agent":           agentUUID,
				"pipeline-max-rounds":    "3",
				"pipeline-product-agent": agentUUID,
			},
			wantErr: "--review-agent and --pipeline-product-agent must differ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newSquadWorkflowPatchTestCmd()
			for name, value := range tt.flags {
				if err := cmd.Flags().Set(name, value); err != nil {
					t.Fatalf("set --%s: %v", name, err)
				}
			}
			patch, err := workflowPatchFromFlags(context.Background(), nil, cmd)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("workflowPatchFromFlags: %v", err)
			}
			if !reflect.DeepEqual(patch, tt.want) {
				t.Fatalf("patch = %#v, want %#v", patch, tt.want)
			}
		})
	}
}

func TestWorkflowPatchFromFlagsResolvesReviewAgentName(t *testing.T) {
	const agentUUID = "33333333-3333-3333-3333-333333333333"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": agentUUID, "name": "包公 - 质量工程师",
		}})
	}))
	defer srv.Close()

	cmd := newSquadWorkflowPatchTestCmd()
	if err := cmd.Flags().Set("review-agent", "包公 - 质量工程师"); err != nil {
		t.Fatalf("set --review-agent: %v", err)
	}
	client := cli.NewAPIClient(srv.URL, "workspace-123", "test-token")
	patch, err := workflowPatchFromFlags(context.Background(), client, cmd)
	if err != nil {
		t.Fatalf("workflowPatchFromFlags: %v", err)
	}
	if got := patch["review_agent_id"]; got != agentUUID {
		t.Fatalf("review_agent_id = %#v, want %q", got, agentUUID)
	}
}
