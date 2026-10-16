package squadworkflow

import "sort"

// Gate identifies an evidence gate that can block stage promotion or final
// delivery. The coordinator deliberately knows only the contract; adapters
// decide how CI, review, merge, and human approval evidence is collected.
type Gate string

const (
	GateTests         Gate = "tests"
	GateReview        Gate = "review"
	GateMerge         Gate = "merge"
	GateHumanApproval Gate = "human_approval"
)

// GateEvidence is the normalized result supplied by an infrastructure
// adapter. A false value means "not proven", not necessarily "failed".
type GateEvidence struct {
	TestsPassed    bool
	ReviewApproved bool
	MergeCompleted bool
	HumanApproved  bool
}

type GateResult struct {
	Passed  bool
	Missing []Gate
}

// GateEvaluator is the dependency-inversion boundary for external evidence.
// A GitHub/CI adapter, a local test runner, or a human approval adapter can be
// introduced without changing the workflow coordinator.
type GateEvaluator interface {
	Evaluate(policy Policy, evidence GateEvidence) GateResult
}

// PolicyGateEvaluator evaluates the gates represented directly by Policy.
// It is intentionally deterministic and infrastructure-free, which makes it a
// safe default for the API and a useful test double for future adapters.
type PolicyGateEvaluator struct{}

func (PolicyGateEvaluator) Evaluate(policy Policy, evidence GateEvidence) GateResult {
	missing := make([]Gate, 0, 4)
	if policy.RequireTests && !evidence.TestsPassed {
		missing = append(missing, GateTests)
	}
	if policy.RequireReview && !evidence.ReviewApproved {
		missing = append(missing, GateReview)
	}
	if policy.RequireMerge && !evidence.MergeCompleted {
		missing = append(missing, GateMerge)
	}
	if policy.HumanApprovalRequired && !evidence.HumanApproved {
		missing = append(missing, GateHumanApproval)
	}
	return GateResult{Passed: len(missing) == 0, Missing: missing}
}

// Child is the persistence-neutral representation required for barrier
// evaluation. Status must already be canonical (custom statuses resolved to
// their category) before it reaches the coordinator.
type Child struct {
	ID     string
	Stage  int32
	Staged bool
	Status string
}

type Transition struct {
	ChildID        string
	PreviousStatus string
	CurrentStatus  string
}

type Decision struct {
	CompletionOpened bool
	StageClosed      bool
	ClosedStage      int32
	NextStage        int32
	Gates            GateResult
	CanPromote       bool
	CanFinalize      bool
}

// Coordinator contains the pure workflow decision engine. It has no database,
// HTTP, queue, or VCS dependencies; callers own those side effects.
type Coordinator struct {
	Gates GateEvaluator
}

func NewCoordinator(gates GateEvaluator) Coordinator {
	if gates == nil {
		gates = PolicyGateEvaluator{}
	}
	return Coordinator{Gates: gates}
}

// EvaluateTransition evaluates one committed child status transition against
// the final sibling snapshot. Stage closure is independent from gate status:
// the parent should still be woken when gates are missing so the coordinator
// can collect evidence, but promotion/finalization remains blocked.
func (c Coordinator) EvaluateTransition(policy Policy, children []Child, transition Transition, evidence GateEvidence) Decision {
	decision := Decision{}
	decision.CompletionOpened = policy.OpensCompletion(transition.PreviousStatus, transition.CurrentStatus)
	if !decision.CompletionOpened {
		decision.Gates = c.gateEvaluator().Evaluate(policy, evidence)
		return decision
	}

	completed, found := findChild(children, transition.ChildID)
	if !found {
		return decision
	}
	if !stageBarrierClosed(policy, children, completed) {
		return decision
	}
	decision.StageClosed = true
	if completed.Staged {
		decision.ClosedStage = completed.Stage
	}
	decision.NextStage = nextStage(policy, children, decision.ClosedStage)
	decision.Gates = c.gateEvaluator().Evaluate(policy, evidence)
	decision.CanPromote = decision.StageClosed && decision.Gates.Passed && decision.NextStage > 0
	decision.CanFinalize = decision.StageClosed && decision.Gates.Passed && decision.NextStage == 0
	return decision
}

func (c Coordinator) gateEvaluator() GateEvaluator {
	if c.Gates == nil {
		return PolicyGateEvaluator{}
	}
	return c.Gates
}

func findChild(children []Child, id string) (Child, bool) {
	for _, child := range children {
		if child.ID == id {
			return child, true
		}
	}
	return Child{}, false
}

func stageBarrierClosed(policy Policy, children []Child, completed Child) bool {
	staged := false
	for _, child := range children {
		if child.Staged {
			staged = true
			break
		}
	}
	if !staged {
		for _, child := range children {
			if !policy.IsCompletionStatus(child.Status) {
				return false
			}
		}
		return true
	}
	if !completed.Staged {
		return false
	}
	for _, child := range children {
		if child.Staged && child.Stage <= completed.Stage && !policy.IsCompletionStatus(child.Status) {
			return false
		}
	}
	return true
}

func nextStage(policy Policy, children []Child, closedStage int32) int32 {
	stages := make([]int32, 0)
	seen := map[int32]bool{}
	for _, child := range children {
		if !child.Staged || child.Stage <= closedStage || policy.IsCompletionStatus(child.Status) || seen[child.Stage] {
			continue
		}
		seen[child.Stage] = true
		stages = append(stages, child.Stage)
	}
	if len(stages) == 0 {
		return 0
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i] < stages[j] })
	return stages[0]
}
