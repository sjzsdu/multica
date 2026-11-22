package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// multica issue verdict — the product role's acceptance gate on a pipeline
// squad. The server, not the prompt, counts rework rounds: each reject increments
// the issue's pipeline_round metadata, and the reject that would exceed the
// squad's --pipeline-max-rounds cap is not dispatched at all — the server
// escalates to a human and reports "escalated": true here. See
// server/internal/handler/issue_pipeline.go.

var issueVerdictCmd = &cobra.Command{
	Use:   "verdict <issue-id> <accept|reject>",
	Short: "Record the pipeline product verdict on an issue",
	Long: `Record the product verdict that closes or reworks a pipeline squad's issue.

  multica issue verdict MUL-12 accept
  multica issue verdict MUL-12 reject --reason "saving settings crashes the page"

Rejects require --reason. The server counts each reject against the squad's
pipeline max rounds; when the next reject would exceed it, the server stops the
loop itself — it notifies the owner, tells the leader to stand down, and answers
with "escalated": true. A rejected verdict also returns the current round, so
the reason and round can be quoted back in the rework request.`,
	Args: cobra.ExactArgs(2),
	RunE: runIssueVerdict,
}

var issueReviewCmd = &cobra.Command{
	Use:   "review <issue-id> <approve|reject>",
	Short: "Record the independent review for a workflow run",
	Long: `Record the configured independent reviewer's decision for a workflow run.

  multica issue review MUL-12 approve --run <run-id> --round 0
  multica issue review MUL-12 reject --run <run-id> --round 0 --reason "checkout regresses"

The round must match the issue's current pipeline round. Rejections require
--reason so the implementation leader receives actionable rework evidence.`,
	Args: cobra.ExactArgs(2),
	RunE: runIssueReview,
}

func init() {
	issueVerdictCmd.Flags().String("reason", "", "Why the build was rejected (required for reject)")
	issueVerdictCmd.Flags().Int("round", 0, "Current pipeline round (required by verdict-enforced squads)")
	issueVerdictCmd.Flags().String("output", "table", "Output format: table or json")
	issueReviewCmd.Flags().String("run", "", "Workflow run ID (required)")
	issueReviewCmd.Flags().Int("round", 0, "Current pipeline round (required)")
	issueReviewCmd.Flags().String("reason", "", "Review note (required for reject)")
	issueReviewCmd.Flags().String("output", "table", "Output format: table or json")
	issueCmd.AddCommand(issueVerdictCmd, issueReviewCmd)
}

func runIssueVerdict(cmd *cobra.Command, args []string) error {
	verdict := strings.ToLower(strings.TrimSpace(args[1]))
	if verdict != "accept" && verdict != "reject" {
		return fmt.Errorf("verdict must be 'accept' or 'reject', got %q", args[1])
	}
	reason, _ := cmd.Flags().GetString("reason")
	reason = strings.TrimSpace(reason)
	if verdict == "reject" && reason == "" {
		return fmt.Errorf("--reason is required when rejecting")
	}
	round, _ := cmd.Flags().GetInt("round")
	if round < 0 {
		return fmt.Errorf("--round must be >= 0")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	path := "/api/issues/" + issueRef.ID + "/pipeline/verdict"
	body := map[string]any{"verdict": verdict, "reason": reason}
	if cmd.Flags().Changed("round") {
		body["round"] = round
	}
	if err := client.PostJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("record verdict: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Verdict:  %s\n", strVal(result, "verdict"))
	fmt.Printf("Round:    %s of %s\n", numberVal(result, "round"), numberVal(result, "max_rounds"))
	if escalated, _ := result["escalated"].(bool); escalated {
		fmt.Printf("Status:   escalated — the pipeline stopped for a human decision\n")
	} else if metadata, ok := result["metadata"].(map[string]any); ok {
		fmt.Printf("Status:   %s\n", strVal(metadata, "pipeline_status"))
	}
	return nil
}

func runIssueReview(cmd *cobra.Command, args []string) error {
	action := strings.ToLower(strings.TrimSpace(args[1]))
	if action != "approve" && action != "reject" {
		return fmt.Errorf("review decision must be 'approve' or 'reject', got %q", args[1])
	}
	runID, _ := cmd.Flags().GetString("run")
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("--run is required")
	}
	if !cmd.Flags().Changed("round") {
		return fmt.Errorf("--round is required")
	}
	round, _ := cmd.Flags().GetInt("round")
	if round < 0 {
		return fmt.Errorf("--round must be >= 0")
	}
	note, _ := cmd.Flags().GetString("reason")
	note = strings.TrimSpace(note)
	if action == "reject" && note == "" {
		return fmt.Errorf("--reason is required when rejecting")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	path := "/api/issues/" + issueRef.ID + "/workflow-runs/" + url.PathEscape(runID) + "/review"
	body := map[string]any{"approved": action == "approve", "note": note, "round": round}
	if err := client.PostJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("record review: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Review:   %s\n", action)
	fmt.Printf("Round:    %s\n", numberVal(result, "pipeline_round"))
	return nil
}

// numberVal renders a JSON number without the float64 ".0" tail.
func numberVal(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case string:
		return v
	default:
		return "-"
	}
}
