package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/presets"
)

func TestParseSelection(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		size    int
		want    []int
		wantErr string
	}{
		{name: "single", input: "2", size: 5, want: []int{1}},
		{name: "list", input: "1,3,5", size: 5, want: []int{0, 2, 4}},
		{name: "range", input: "2-4", size: 5, want: []int{1, 2, 3}},
		{name: "mixed", input: "1,3-4", size: 5, want: []int{0, 2, 3}},
		{name: "dedupe", input: "1,1,1-2", size: 5, want: []int{0, 1}},
		{name: "out of range", input: "6", size: 5, wantErr: "out of range"},
		{name: "bad range", input: "4-2", size: 5, wantErr: "invalid range"},
		{name: "empty", input: " , ", size: 5, wantErr: "no items selected"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSelection(tc.input, tc.size)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSelection: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func blackboxOnboardTestWorkflow() map[string]any {
	return map[string]any{
		"mode":                     "delivery",
		"max_active_children":      1,
		"child_completion_signal":  "review_ready",
		"auto_promote_stage":       true,
		"auto_finalize_parent":     true,
		"require_tests":            true,
		"require_review":           true,
		"require_merge":            true,
		"merge_mode":               "integrator",
		"final_gates_only":         true,
		"review_agent_id":          "{{agent-id:Reviewer}}",
		"require_pipeline_verdict": true,
		"pipeline": map[string]any{
			"max_rounds":       3,
			"product_agent_id": "{{agent-id:Product}}",
		},
	}
}

func TestResolveOnboardWorkflowAgentIDs(t *testing.T) {
	productID := uuid.NewString()
	reviewerID := uuid.NewString()
	workflow, err := resolveOnboardWorkflowAgentIDs(blackboxOnboardTestWorkflow(), map[string]string{
		"Product": productID, "Reviewer": reviewerID,
	})
	if err != nil {
		t.Fatalf("resolveOnboardWorkflowAgentIDs: %v", err)
	}
	if got := workflow["review_agent_id"]; got != reviewerID {
		t.Fatalf("review_agent_id = %v, want %s", got, reviewerID)
	}
	pipeline, ok := workflow["pipeline"].(map[string]any)
	if !ok {
		t.Fatalf("pipeline = %#v, want object", workflow["pipeline"])
	}
	if got := pipeline["product_agent_id"]; got != productID {
		t.Fatalf("product_agent_id = %v, want %s", got, productID)
	}
}

func TestResolveOnboardWorkflowAgentIDsRejectsUnknownAgent(t *testing.T) {
	_, err := resolveOnboardWorkflowAgentIDs(blackboxOnboardTestWorkflow(), map[string]string{
		"Product": uuid.NewString(),
	})
	if err == nil || !strings.Contains(err.Error(), "Reviewer") {
		t.Fatalf("error = %v, want unknown Reviewer", err)
	}
}

func TestSymbolicOnboardAgentIDsAreDeterministicAndDistinct(t *testing.T) {
	agents := []presets.Agent{{Name: "Product"}, {Name: "Reviewer"}}
	first := symbolicOnboardAgentIDs(agents)
	second := symbolicOnboardAgentIDs(agents)
	if first["Product"] != second["Product"] || first["Reviewer"] != second["Reviewer"] {
		t.Fatalf("symbolic ids changed between calls: %v != %v", first, second)
	}
	if first["Product"] == first["Reviewer"] {
		t.Fatalf("different agents share symbolic id %q", first["Product"])
	}
	for name, id := range first {
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("%s symbolic id %q is not a UUID: %v", name, id, err)
		}
	}
}

func TestValidateOnboardSelectionChecksWorkflowAgentClosure(t *testing.T) {
	selection := onboardSelection{
		agents: []presets.Agent{{Name: "Product"}},
		squads: []presets.Squad{{
			Name: "Blackbox", Leader: "Product", Workflow: blackboxOnboardTestWorkflow(),
		}},
	}
	err := validateOnboardSelection(selection, map[string]presets.Skill{}, symbolicOnboardAgentIDs(selection.agents))
	if err == nil || !strings.Contains(err.Error(), "Reviewer") {
		t.Fatalf("error = %v, want missing Reviewer", err)
	}
}

func TestValidateOnboardSelectionRejectsInvalidWorkflowPolicy(t *testing.T) {
	workflow := blackboxOnboardTestWorkflow()
	workflow["pipeline"].(map[string]any)["max_rounds"] = 0
	selection := onboardSelection{
		agents: []presets.Agent{{Name: "Product"}, {Name: "Reviewer"}},
		squads: []presets.Squad{{Name: "Blackbox", Leader: "Product", Workflow: workflow}},
	}
	err := validateOnboardSelection(selection, map[string]presets.Skill{}, symbolicOnboardAgentIDs(selection.agents))
	if err == nil || !strings.Contains(err.Error(), "max_rounds") {
		t.Fatalf("error = %v, want max_rounds validation", err)
	}
}

func TestChoosePresetsScopesAllItemsToNamedPreset(t *testing.T) {
	catalog := presets.Catalog{Presets: []presets.Preset{{ID: "first"}, {ID: "second"}}}
	cmd := newOnboardTestCommand()
	_ = cmd.Flags().Set("preset", "second")
	chosen, err := choosePresets(cmd, catalog, &onboardPrompt{}, true)
	if err != nil {
		t.Fatalf("choosePresets: %v", err)
	}
	if len(chosen) != 1 || chosen[0].ID != "second" {
		t.Fatalf("chosen = %+v, want only second", chosen)
	}
}

func TestEnsureSquadResolvesWorkflowAgentIDs(t *testing.T) {
	productID := uuid.NewString()
	reviewerID := uuid.NewString()
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/squads" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode squad request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid.NewString()})
	}))
	t.Cleanup(srv.Close)
	runner := &onboardRunner{
		ctx: context.Background(), client: cli.NewAPIClient(srv.URL, "workspace", "token"),
		agentIDs: map[string]string{"Product": productID, "Reviewer": reviewerID},
		squadIDs: map[string]string{}, createdInRun: map[string]bool{},
	}
	runner.ensureSquad(presets.Squad{
		Name: "Blackbox", Leader: "Product", Workflow: blackboxOnboardTestWorkflow(),
	})
	if len(runner.failures) != 0 {
		t.Fatalf("ensureSquad failures = %v", runner.failures)
	}
	workflow, ok := request["workflow"].(map[string]any)
	if !ok {
		t.Fatalf("workflow = %#v, want object", request["workflow"])
	}
	if got := workflow["review_agent_id"]; got != reviewerID {
		t.Fatalf("review_agent_id = %v, want %s", got, reviewerID)
	}
	pipeline := workflow["pipeline"].(map[string]any)
	if got := pipeline["product_agent_id"]; got != productID {
		t.Fatalf("product_agent_id = %v, want %s", got, productID)
	}
}

func newOnboardTestCommand() *cobra.Command {
	cmd := testCmd()
	flags := cmd.Flags()
	flags.Bool("list", false, "")
	flags.String("preset", "", "")
	flags.Bool("all", false, "")
	flags.String("runtime-id", "", "")
	flags.String("model", "", "")
	flags.Bool("yes", false, "")
	flags.Bool("dry-run", false, "")
	flags.Bool("no-autopilots", false, "")
	return cmd
}

func TestRunOnboardDryRunDoesNotContactAPI(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{
		"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_DAEMON_PORT",
		"MULTICA_TASK_CONFIG_ROOT", "MULTICA_TASK_WORKSPACES_ROOT",
		"MULTICA_WORKSPACE_ID", "MULTICA_TOKEN",
	} {
		t.Setenv(name, "")
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "dry run contacted the API", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newOnboardTestCommand()
	_ = cmd.Flags().Set("preset", "solo-company")
	_ = cmd.Flags().Set("all", "true")
	_ = cmd.Flags().Set("yes", "true")
	_ = cmd.Flags().Set("dry-run", "true")
	stderr := captureStderr(t)
	defer stderr.restore()
	err := runOnboard(cmd, nil)
	output := stderr.read()
	if err != nil {
		t.Fatalf("runOnboard dry run: %v", err)
	}
	if requests != 0 {
		t.Fatalf("dry run API requests = %d, want 0", requests)
	}
	const note = "Dry run: existing resources were not queried; plan counts show all items as new."
	if !strings.Contains(output, note) {
		t.Fatalf("dry run output missing %q:\n%s", note, output)
	}
}

func TestRunOnboardValidatesAgainstExistingWorkspaceAgents(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{
		"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_DAEMON_PORT",
		"MULTICA_TASK_CONFIG_ROOT", "MULTICA_TASK_WORKSPACES_ROOT",
	} {
		t.Setenv(name, "")
	}

	productID := uuid.NewString()
	reviewerID := uuid.NewString()
	originalLoad := loadOnboardCatalog
	loadOnboardCatalog = func() (presets.Catalog, error) {
		return presets.Catalog{Presets: []presets.Preset{{
			ID:     "external-reviewer",
			Agents: []presets.Agent{{Name: "Product"}},
			Squads: []presets.Squad{{
				Name: "Blackbox", Leader: "Product", Workflow: blackboxOnboardTestWorkflow(),
			}},
		}}}, nil
	}
	t.Cleanup(func() { loadOnboardCatalog = originalLoad })

	var squadRequest map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/skills":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "GET /api/agents":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "Reviewer", "id": reviewerID}})
		case "GET /api/squads":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "POST /api/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": productID})
		case "POST /api/squads":
			if err := json.NewDecoder(r.Body).Decode(&squadRequest); err != nil {
				t.Errorf("decode squad request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid.NewString()})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")

	cmd := newOnboardTestCommand()
	_ = cmd.Flags().Set("preset", "external-reviewer")
	_ = cmd.Flags().Set("all", "true")
	_ = cmd.Flags().Set("yes", "true")
	_ = cmd.Flags().Set("runtime-id", "runtime-1")
	if err := runOnboard(cmd, nil); err != nil {
		t.Fatalf("runOnboard with an existing workflow reviewer: %v", err)
	}
	workflow, ok := squadRequest["workflow"].(map[string]any)
	if !ok {
		t.Fatalf("workflow = %#v, want object", squadRequest["workflow"])
	}
	if got := workflow["review_agent_id"]; got != reviewerID {
		t.Fatalf("review_agent_id = %v, want existing reviewer %s", got, reviewerID)
	}
}

func TestChooseItemsPullsSquadMembersAndAutopilotAgents(t *testing.T) {
	agents := []presets.Agent{
		{Name: "leader"},
		{Name: "member"},
		{Name: "operator"},
	}
	catalog := []presets.Preset{{
		ID:     "demo",
		Agents: agents,
		Squads: []presets.Squad{{
			Name:    "team",
			Leader:  "leader",
			Members: []presets.SquadMember{{Name: "leader", Role: "leader"}, {Name: "member", Role: "member"}},
		}},
		Autopilots: []presets.Autopilot{{Title: "nightly", Agent: "operator"}},
	}}

	selection, err := chooseItems(catalog, &onboardPrompt{in: bufio.NewReader(strings.NewReader(""))}, true, false)
	if err != nil {
		t.Fatalf("chooseItems: %v", err)
	}
	if len(selection.squads) != 1 || len(selection.autopilots) != 1 {
		t.Fatalf("selection = %+v", selection)
	}

	// Selecting only the squad must pull in its member agents.
	squadOnly := onboardSelection{
		agents: agents,
		squads: catalog[0].Squads,
	}
	if names := squadAgentNames(squadOnly.squads[0]); len(names) != 2 {
		t.Fatalf("squadAgentNames = %v", names)
	}
}
