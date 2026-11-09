package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var squadCmd = &cobra.Command{
	Use:   "squad",
	Short: "Work with squads",
}

// ── List ────────────────────────────────────────────────────────────────────

var squadListCmd = &cobra.Command{
	Use:   "list",
	Short: "List squads in the workspace",
	Args:  cobra.NoArgs,
	RunE:  runSquadList,
}

func runSquadList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var squads []map[string]any
	if err := client.GetJSON(ctx, "/api/squads", &squads); err != nil {
		return fmt.Errorf("list squads: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, squads)
	}

	if len(squads) == 0 {
		fmt.Fprintln(os.Stderr, "No squads found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tLEADER ID\tMEMBERS")
	for _, s := range squads {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			strVal(s, "id"), strVal(s, "name"), strVal(s, "leader_id"),
			memberCountDisplay(s))
	}
	return w.Flush()
}

func memberCountDisplay(m map[string]any) string {
	v, ok := m["member_count"]
	if !ok || v == nil {
		return "-"
	}
	n, ok := v.(float64)
	if !ok || n <= 0 {
		return "-"
	}
	return strconv.Itoa(int(n))
}

func workflowPatchFromFlags(ctx context.Context, client *cli.APIClient, cmd *cobra.Command) (map[string]any, error) {
	patch := map[string]any{}
	if cmd.Flags().Changed("workflow-mode") {
		v, _ := cmd.Flags().GetString("workflow-mode")
		patch["mode"] = v
	}
	if cmd.Flags().Changed("child-completion-signal") {
		v, _ := cmd.Flags().GetString("child-completion-signal")
		patch["child_completion_signal"] = v
	}
	if cmd.Flags().Changed("max-active-children") {
		v, _ := cmd.Flags().GetInt("max-active-children")
		patch["max_active_children"] = v
	}
	for _, name := range []string{"auto-promote-stage", "auto-finalize-parent", "require-tests", "require-review", "require-merge", "human-approval-required"} {
		if cmd.Flags().Changed(name) {
			v, _ := cmd.Flags().GetBool(name)
			patch[nameToWorkflowKey(name)] = v
		}
	}
	if cmd.Flags().Changed("merge-mode") {
		v, _ := cmd.Flags().GetString("merge-mode")
		patch["merge_mode"] = v
	}
	if err := applyPipelineFlags(ctx, client, cmd, patch); err != nil {
		return nil, err
	}
	return patch, nil
}

// applyPipelineFlags adds the `pipeline` block to a workflow patch. The block is
// replaced wholesale on the server, so it is only built when one of the two
// flags is present: --pipeline-max-rounds alone restates the block without a
// product agent (any squad member may verdict), and max rounds 0 clears the
// block, which turns the verdict endpoint off again.
func applyPipelineFlags(ctx context.Context, client *cli.APIClient, cmd *cobra.Command, patch map[string]any) error {
	roundsSet := cmd.Flags().Changed("pipeline-max-rounds")
	agentSet := cmd.Flags().Changed("pipeline-product-agent")
	if !roundsSet && !agentSet {
		return nil
	}
	if !roundsSet {
		return fmt.Errorf("--pipeline-max-rounds is required with --pipeline-product-agent; the pipeline block replaces the stored one")
	}
	rounds, _ := cmd.Flags().GetInt("pipeline-max-rounds")
	if rounds < 0 {
		return fmt.Errorf("--pipeline-max-rounds must be >= 0")
	}
	agent, _ := cmd.Flags().GetString("pipeline-product-agent")
	if rounds == 0 {
		if agent != "" {
			return fmt.Errorf("--pipeline-product-agent requires --pipeline-max-rounds above 0")
		}
		patch["pipeline"] = nil
		return nil
	}
	pipeline := map[string]any{"max_rounds": rounds}
	if agent != "" {
		agentID, err := resolveAgent(ctx, client, agent)
		if err != nil {
			return fmt.Errorf("resolve pipeline product agent: %w", err)
		}
		pipeline["product_agent_id"] = agentID
	}
	patch["pipeline"] = pipeline
	return nil
}

func nameToWorkflowKey(name string) string {
	keys := map[string]string{
		"auto-promote-stage":      "auto_promote_stage",
		"auto-finalize-parent":    "auto_finalize_parent",
		"require-tests":           "require_tests",
		"require-review":          "require_review",
		"require-merge":           "require_merge",
		"human-approval-required": "human_approval_required",
	}
	return keys[name]
}

func addSquadWorkflowFlags(cmd *cobra.Command) {
	cmd.Flags().String("workflow-mode", "", "Workflow mode: manual, staged, or delivery")
	cmd.Flags().String("child-completion-signal", "", "Child completion signal: terminal_only or review_ready")
	cmd.Flags().Int("max-active-children", -1, "Maximum active child issues; 0 means unlimited")
	cmd.Flags().Bool("auto-promote-stage", false, "Automatically promote the next stage")
	cmd.Flags().Bool("auto-finalize-parent", false, "Automatically move the parent to in_review after final gates")
	cmd.Flags().Bool("require-tests", false, "Require test evidence before delivery")
	cmd.Flags().Bool("require-review", false, "Require reviewer approval before delivery")
	cmd.Flags().Bool("require-merge", false, "Require merge before delivery")
	cmd.Flags().String("merge-mode", "", "Merge mode: human, integrator, or pr_close_intent")
	cmd.Flags().Bool("human-approval-required", false, "Require human approval before final delivery")
	cmd.Flags().Int("pipeline-max-rounds", -1, "Pipeline rework rounds before the server escalates to a human; 0 turns the pipeline off. Replaces the stored pipeline block, so restate --pipeline-product-agent with it")
	cmd.Flags().String("pipeline-product-agent", "", "Agent name or ID allowed to record pipeline verdicts (requires --pipeline-max-rounds above 0); empty lets any squad member verdict")
}

// ── Get ─────────────────────────────────────────────────────────────────────

var squadGetCmd = &cobra.Command{
	Use:   "get <squad-id>",
	Short: "Get squad details",
	Args:  exactArgs(1),
	RunE:  runSquadGet,
}

func runSquadGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var squad map[string]any
	if err := client.GetJSON(ctx, "/api/squads/"+args[0], &squad); err != nil {
		return fmt.Errorf("get squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, squad)
	}

	fmt.Printf("ID:           %s\n", strVal(squad, "id"))
	fmt.Printf("Name:         %s\n", strVal(squad, "name"))
	fmt.Printf("Description:  %s\n", strVal(squad, "description"))
	fmt.Printf("Leader ID:    %s\n", strVal(squad, "leader_id"))
	fmt.Printf("Created:      %s\n", strVal(squad, "created_at"))
	if inst := strVal(squad, "instructions"); inst != "" {
		fmt.Printf("Instructions: %s\n", inst)
	}
	if workflow, ok := squad["workflow"].(map[string]any); ok {
		fmt.Printf("Workflow:     %s (completion=%s, max_children=%v)\n",
			strVal(workflow, "mode"), strVal(workflow, "child_completion_signal"), workflow["max_active_children"])
		if pipeline, ok := workflow["pipeline"].(map[string]any); ok {
			product := strVal(pipeline, "product_agent_id")
			if product == "" {
				product = "any squad member"
			}
			fmt.Printf("Pipeline:     max_rounds=%v, verdicts by %s\n", pipeline["max_rounds"], product)
		}
	}
	return nil
}

// ── Create ──────────────────────────────────────────────────────────────────

var squadCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new squad",
	Args:  cobra.NoArgs,
	RunE:  runSquadCreate,
}

func runSquadCreate(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	leader, _ := cmd.Flags().GetString("leader")
	if leader == "" {
		return fmt.Errorf("--leader is required (agent name or ID)")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	leaderID, err := resolveAgent(ctx, client, leader)
	if err != nil {
		return fmt.Errorf("resolve leader: %w", err)
	}

	body := map[string]any{
		"name":      name,
		"leader_id": leaderID,
	}
	if v, _ := cmd.Flags().GetString("description"); v != "" {
		body["description"] = v
	}
	workflow, err := workflowPatchFromFlags(ctx, client, cmd)
	if err != nil {
		return err
	}
	if len(workflow) > 0 {
		body["workflow"] = workflow
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/squads", body, &result); err != nil {
		return fmt.Errorf("create squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Squad created: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

// ── Update ──────────────────────────────────────────────────────────────────

var squadUpdateCmd = &cobra.Command{
	Use:   "update <squad-id>",
	Short: "Update a squad",
	Args:  exactArgs(1),
	RunE:  runSquadUpdate,
}

func runSquadUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		body["name"] = v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	if cmd.Flags().Changed("instructions") {
		v, _ := cmd.Flags().GetString("instructions")
		body["instructions"] = v
	}
	if cmd.Flags().Changed("leader") {
		v, _ := cmd.Flags().GetString("leader")
		leaderID, err := resolveAgent(ctx, client, v)
		if err != nil {
			return fmt.Errorf("resolve leader: %w", err)
		}
		body["leader_id"] = leaderID
	}
	if cmd.Flags().Changed("avatar-url") {
		v, _ := cmd.Flags().GetString("avatar-url")
		body["avatar_url"] = v
	}
	workflow, err := workflowPatchFromFlags(ctx, client, cmd)
	if err != nil {
		return err
	}
	if len(workflow) > 0 {
		body["workflow"] = workflow
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use flags like --name, --description, --instructions, --leader, or --workflow-mode")
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/squads/"+args[0], body, &result); err != nil {
		return fmt.Errorf("update squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Squad updated: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

// ── Delete ──────────────────────────────────────────────────────────────────

var squadDeleteCmd = &cobra.Command{
	Use:   "delete <squad-id>",
	Short: "Delete (archive) a squad",
	Args:  exactArgs(1),
	RunE:  runSquadDelete,
}

func runSquadDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/squads/"+args[0]); err != nil {
		return fmt.Errorf("delete squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"id": args[0], "deleted": true})
	}
	fmt.Fprintf(os.Stderr, "Squad %s deleted.\n", args[0])
	return nil
}

// ── Members ─────────────────────────────────────────────────────────────────

var squadMemberCmd = &cobra.Command{
	Use:   "member",
	Short: "Work with squad members",
}

var squadMemberListCmd = &cobra.Command{
	Use:   "list <squad-id>",
	Short: "List members of a squad",
	Args:  exactArgs(1),
	RunE:  runSquadMemberList,
}

func runSquadMemberList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var members []map[string]any
	if err := client.GetJSON(ctx, "/api/squads/"+args[0]+"/members", &members); err != nil {
		return fmt.Errorf("list members: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, members)
	}

	if len(members) == 0 {
		fmt.Fprintln(os.Stderr, "No members found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MEMBER ID\tTYPE\tROLE")
	for _, m := range members {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			strVal(m, "member_id"), strVal(m, "member_type"), strVal(m, "role"))
	}
	return w.Flush()
}

// ── Member Add ──────────────────────────────────────────────────────────────

var squadMemberAddCmd = &cobra.Command{
	Use:   "add <squad-id>",
	Short: "Add a member to a squad",
	Args:  exactArgs(1),
	RunE:  runSquadMemberAdd,
}

func runSquadMemberAdd(cmd *cobra.Command, args []string) error {
	memberID, _ := cmd.Flags().GetString("member-id")
	memberType, _ := cmd.Flags().GetString("type")
	role, _ := cmd.Flags().GetString("role")

	if memberID == "" {
		return fmt.Errorf("--member-id is required")
	}
	if memberType != "agent" && memberType != "member" {
		return fmt.Errorf("--type must be 'agent' or 'member'")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{
		"member_type": memberType,
		"member_id":   memberID,
		"role":        role,
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/squads/"+args[0]+"/members", body, &result); err != nil {
		return fmt.Errorf("add member: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Member %s added to squad.\n", memberID)
	return nil
}

// ── Member Set Role ─────────────────────────────────────────────────────────

var squadMemberSetRoleCmd = &cobra.Command{
	Use:   "set-role <squad-id>",
	Short: "Change a squad member's role",
	Args:  exactArgs(1),
	RunE:  runSquadMemberSetRole,
}

func runSquadMemberSetRole(cmd *cobra.Command, args []string) error {
	memberID, _ := cmd.Flags().GetString("member-id")
	memberType, _ := cmd.Flags().GetString("member-type")
	role, _ := cmd.Flags().GetString("role")

	if memberID == "" {
		return fmt.Errorf("--member-id is required")
	}
	if memberType != "agent" && memberType != "member" {
		return fmt.Errorf("--member-type must be 'agent' or 'member'")
	}
	if role == "" {
		return fmt.Errorf("--role is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{
		"member_type": memberType,
		"member_id":   memberID,
		"role":        role,
	}

	var result map[string]any
	if err := client.PatchJSON(ctx, "/api/squads/"+args[0]+"/members/role", body, &result); err != nil {
		return fmt.Errorf("set member role: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Fprintf(os.Stderr, "Member %s role updated to %s.\n", memberID, role)
	return nil
}

// ── Member Remove ───────────────────────────────────────────────────────────

var squadMemberRemoveCmd = &cobra.Command{
	Use:   "remove <squad-id>",
	Short: "Remove a member from a squad",
	Args:  exactArgs(1),
	RunE:  runSquadMemberRemove,
}

func runSquadMemberRemove(cmd *cobra.Command, args []string) error {
	memberID, _ := cmd.Flags().GetString("member-id")
	memberType, _ := cmd.Flags().GetString("type")

	if memberID == "" {
		return fmt.Errorf("--member-id is required")
	}
	if memberType != "agent" && memberType != "member" {
		return fmt.Errorf("--type must be 'agent' or 'member'")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{
		"member_type": memberType,
		"member_id":   memberID,
	}

	if err := client.DeleteJSONWithBody(ctx, "/api/squads/"+args[0]+"/members", body); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"squad_id": args[0], "member_id": memberID, "removed": true})
	}
	fmt.Fprintf(os.Stderr, "Member %s removed from squad.\n", memberID)
	return nil
}

// ── Activity ────────────────────────────────────────────────────────────────

var squadActivityCmd = &cobra.Command{
	Use:   "activity <issue-id> <outcome>",
	Short: "Record a squad leader evaluation on an issue",
	Long: `Record the squad leader's evaluation decision for an issue.

Outcome must be one of:
  action     — leader delegated or took action
  no_action  — leader evaluated and decided no action needed
  failed     — leader encountered an error

This command is intended to be called by squad leader agents after each
trigger to record their decision in the issue timeline.

Pass the issue the current turn is running on. That issue does not need to
be assigned to your squad — authorization comes from the leader task itself,
so @squad mentions on an individually owned issue and leader tasks bound to a
child issue both record fine. A leader woken by a stage barrier runs on the
parent issue, so record against the parent.`,
	Args: exactArgs(2),
	RunE: runSquadActivity,
}

func runSquadActivity(cmd *cobra.Command, args []string) error {
	issueID := args[0]
	outcome := args[1]

	if outcome != "action" && outcome != "no_action" && outcome != "failed" {
		return fmt.Errorf("invalid outcome %q; valid values: action, no_action, failed", outcome)
	}

	reason, _ := cmd.Flags().GetString("reason")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, issueID)
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{
		"outcome": outcome,
		"reason":  reason,
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/squad-evaluated", body, &result); err != nil {
		return fmt.Errorf("record evaluation: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Squad evaluation recorded: %s (issue %s)\n", outcome, issueRef.Display)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	return nil
}

// ── Init ────────────────────────────────────────────────────────────────────

func init() {
	// list
	squadListCmd.Flags().String("output", "table", "Output format: table or json")

	// get
	squadGetCmd.Flags().String("output", "table", "Output format: table or json")

	// create
	squadCreateCmd.Flags().String("name", "", "Squad name (required)")
	squadCreateCmd.Flags().String("description", "", "Squad description")
	squadCreateCmd.Flags().String("leader", "", "Leader agent (name or ID) — required")
	squadCreateCmd.Flags().String("output", "json", "Output format: table or json")
	addSquadWorkflowFlags(squadCreateCmd)

	// update
	squadUpdateCmd.Flags().String("name", "", "New name")
	squadUpdateCmd.Flags().String("description", "", "New description")
	squadUpdateCmd.Flags().String("instructions", "", "New instructions")
	squadUpdateCmd.Flags().String("leader", "", "New leader agent (name or ID)")
	squadUpdateCmd.Flags().String("avatar-url", "", "New avatar URL")
	squadUpdateCmd.Flags().String("output", "json", "Output format: table or json")
	addSquadWorkflowFlags(squadUpdateCmd)

	// delete
	squadDeleteCmd.Flags().String("output", "table", "Output format: table or json")

	// member list
	squadMemberListCmd.Flags().String("output", "table", "Output format: table or json")

	// member add
	squadMemberAddCmd.Flags().String("member-id", "", "Member or agent ID (required)")
	squadMemberAddCmd.Flags().String("type", "agent", "Member type: agent or member")
	squadMemberAddCmd.Flags().String("role", "member", "Role in the squad")
	squadMemberAddCmd.Flags().String("output", "json", "Output format: table or json")

	// member remove
	squadMemberRemoveCmd.Flags().String("member-id", "", "Member or agent ID (required)")
	squadMemberRemoveCmd.Flags().String("type", "agent", "Member type: agent or member")
	squadMemberRemoveCmd.Flags().String("output", "table", "Output format: table or json")

	// member set-role
	squadMemberSetRoleCmd.Flags().String("member-id", "", "Member or agent ID (required)")
	squadMemberSetRoleCmd.Flags().String("member-type", "agent", "Member type: agent or member")
	squadMemberSetRoleCmd.Flags().String("role", "", "New role in the squad (required)")
	squadMemberSetRoleCmd.Flags().String("output", "json", "Output format: table or json")

	// activity
	squadActivityCmd.Flags().String("reason", "", "Short explanation of the decision")
	squadActivityCmd.Flags().String("output", "table", "Output format: table or json")

	squadMemberCmd.AddCommand(squadMemberListCmd)
	squadMemberCmd.AddCommand(squadMemberAddCmd)
	squadMemberCmd.AddCommand(squadMemberRemoveCmd)
	squadMemberCmd.AddCommand(squadMemberSetRoleCmd)

	squadCmd.AddCommand(squadListCmd)
	squadCmd.AddCommand(squadGetCmd)
	squadCmd.AddCommand(squadCreateCmd)
	squadCmd.AddCommand(squadUpdateCmd)
	squadCmd.AddCommand(squadDeleteCmd)
	squadCmd.AddCommand(squadMemberCmd)
	squadCmd.AddCommand(squadActivityCmd)
}
