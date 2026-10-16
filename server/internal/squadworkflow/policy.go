package squadworkflow

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/internal/util"
)

// Mode controls how a squad coordinates work after the leader dispatches it.
// Manual preserves the historical leader-driven behavior.
type Mode string

const (
	ModeManual   Mode = "manual"
	ModeStaged   Mode = "staged"
	ModeDelivery Mode = "delivery"
)

// CompletionSignal controls the status transition that can close a child
// stage. TerminalOnly is the backwards-compatible behavior. ReviewReady lets
// an automated squad continue from the normal agent hand-off status without
// requiring a human to flip every child to done first.
type CompletionSignal string

const (
	CompletionSignalTerminalOnly CompletionSignal = "terminal_only"
	CompletionSignalReviewReady  CompletionSignal = "review_ready"
)

type MergeMode string

const (
	MergeModeHuman       MergeMode = "human"
	MergeModeIntegrator  MergeMode = "integrator"
	MergeModeCloseIntent MergeMode = "pr_close_intent"
)

// Policy is the durable, versionable workflow contract for a squad. Keep this
// as a domain value object: HTTP, CLI and persistence layers should not each
// invent their own interpretation of these fields.
// PipelinePolicy configures the unattended iteration pipeline a squad can
// run: the product-role agent records accept/reject verdicts through the
// server-side verdict endpoint (POST /api/issues/{id}/pipeline/verdict),
// which counts reject rounds against MaxRounds and escalates to a human once
// a rejection would exceed the cap. The presence of this block on a squad is
// what enables that endpoint; squads without it refuse every verdict.
type PipelinePolicy struct {
	// MaxRounds is how many rework rounds a rejection may start. Reject
	// number MaxRounds+1 escalates to a human instead of dispatching again.
	MaxRounds int `json:"max_rounds"`
	// ProductAgentID optionally restricts verdicts to the one agent playing
	// the pipeline's product role. Empty lets any member of the squad verdict.
	// The setup script writes the created agent's UUID here.
	ProductAgentID string `json:"product_agent_id,omitempty"`
}

type Policy struct {
	Mode                  Mode             `json:"mode"`
	MaxActiveChildren     int              `json:"max_active_children"`
	ChildCompletionSignal CompletionSignal `json:"child_completion_signal"`
	AutoPromoteStage      bool             `json:"auto_promote_stage"`
	AutoFinalizeParent    bool             `json:"auto_finalize_parent"`
	RequireTests          bool             `json:"require_tests"`
	RequireReview         bool             `json:"require_review"`
	RequireMerge          bool             `json:"require_merge"`
	MergeMode             MergeMode        `json:"merge_mode"`
	HumanApprovalRequired bool             `json:"human_approval_required"`
	// Pipeline is absent (nil) for ordinary squads. omitempty keeps stored
	// config and briefings free of the block until a pipeline is configured,
	// and Parse below restores it from stored JSON like every other field.
	Pipeline *PipelinePolicy `json:"pipeline,omitempty"`
}

func DefaultPolicy() Policy {
	return Policy{
		Mode:                  ModeManual,
		MaxActiveChildren:     0,
		ChildCompletionSignal: CompletionSignalTerminalOnly,
		MergeMode:             MergeModeHuman,
		HumanApprovalRequired: true,
	}
}

func (p Policy) Validate() error {
	switch p.Mode {
	case ModeManual, ModeStaged, ModeDelivery:
	default:
		return fmt.Errorf("workflow mode must be one of manual, staged, delivery")
	}
	if p.MaxActiveChildren < 0 {
		return fmt.Errorf("max_active_children must be >= 0")
	}
	switch p.ChildCompletionSignal {
	case CompletionSignalTerminalOnly, CompletionSignalReviewReady:
	default:
		return fmt.Errorf("child_completion_signal must be one of terminal_only, review_ready")
	}
	switch p.MergeMode {
	case MergeModeHuman, MergeModeIntegrator, MergeModeCloseIntent:
	default:
		return fmt.Errorf("merge_mode must be one of human, integrator, pr_close_intent")
	}
	if p.Pipeline != nil {
		if p.Pipeline.MaxRounds < 1 {
			return fmt.Errorf("pipeline max_rounds must be >= 1")
		}
		if p.Pipeline.ProductAgentID != "" {
			if _, err := util.ParseUUID(p.Pipeline.ProductAgentID); err != nil {
				return fmt.Errorf("pipeline product_agent_id must be a UUID")
			}
		}
	}
	return nil
}

// Parse decodes a stored or API-supplied configuration. An empty value means
// the historical default, which keeps old rows and old clients compatible.
func Parse(raw []byte) (Policy, error) {
	p := DefaultPolicy()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &p); err != nil {
			return Policy{}, fmt.Errorf("decode workflow policy: %w", err)
		}
	}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func Marshal(p Policy) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode workflow policy: %w", err)
	}
	return b, nil
}

// Merge applies a partial API update to an existing policy. Keeping the merge
// here prevents HTTP and CLI callers from accidentally resetting omitted
// workflow fields to defaults.
func Merge(base, patch []byte) ([]byte, Policy, error) {
	basePolicy, err := Parse(base)
	if err != nil {
		return nil, Policy{}, err
	}
	baseJSON, err := Marshal(basePolicy)
	if err != nil {
		return nil, Policy{}, err
	}
	var baseMap, patchMap map[string]json.RawMessage
	if err := json.Unmarshal(baseJSON, &baseMap); err != nil {
		return nil, Policy{}, fmt.Errorf("decode base workflow policy: %w", err)
	}
	if err := json.Unmarshal(patch, &patchMap); err != nil {
		return nil, Policy{}, fmt.Errorf("decode workflow policy patch: %w", err)
	}
	for key, value := range patchMap {
		baseMap[key] = value
	}
	merged, err := json.Marshal(baseMap)
	if err != nil {
		return nil, Policy{}, fmt.Errorf("encode workflow policy patch: %w", err)
	}
	policy, err := Parse(merged)
	if err != nil {
		return nil, Policy{}, err
	}
	normalized, err := Marshal(policy)
	return normalized, policy, err
}

func (p Policy) IsCompletionStatus(status string) bool {
	if status == "done" || status == "cancelled" {
		return true
	}
	return p.ChildCompletionSignal == CompletionSignalReviewReady && status == "in_review"
}

// IsActiveChildStatus reports whether a child consumes a workflow execution
// slot. Backlog and blocked children are parked; terminal children no longer
// consume capacity. Status must be canonical before calling this method.
func IsActiveChildStatus(status string) bool {
	switch status {
	case "todo", "in_progress", "in_review":
		return true
	default:
		return false
	}
}

func (p Policy) OpensCompletion(prevStatus, currentStatus string) bool {
	return !p.IsCompletionStatus(prevStatus) && p.IsCompletionStatus(currentStatus)
}
