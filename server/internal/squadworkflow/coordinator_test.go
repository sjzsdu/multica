package squadworkflow

import "testing"

func TestCoordinatorDefaultRequiresTerminalChildren(t *testing.T) {
	coordinator := NewCoordinator(nil)
	policy := DefaultPolicy()
	children := []Child{
		{ID: "a", Status: "done"},
		{ID: "b", Status: "in_progress"},
	}
	decision := coordinator.EvaluateTransition(policy, children, Transition{
		ChildID:        "a",
		PreviousStatus: "in_progress",
		CurrentStatus:  "done",
	}, GateEvidence{})
	if !decision.CompletionOpened || decision.StageClosed {
		t.Fatalf("decision = %+v, want opened but not closed", decision)
	}
}

func TestCoordinatorReviewReadyClosesStageAndFindsNextStage(t *testing.T) {
	coordinator := NewCoordinator(nil)
	policy := DefaultPolicy()
	policy.Mode = ModeStaged
	policy.ChildCompletionSignal = CompletionSignalReviewReady
	policy.HumanApprovalRequired = false
	children := []Child{
		{ID: "a", Stage: 1, Staged: true, Status: "in_review"},
		{ID: "b", Stage: 1, Staged: true, Status: "done"},
		{ID: "c", Stage: 2, Staged: true, Status: "backlog"},
	}
	decision := coordinator.EvaluateTransition(policy, children, Transition{
		ChildID:        "a",
		PreviousStatus: "in_progress",
		CurrentStatus:  "in_review",
	}, GateEvidence{})
	if !decision.StageClosed || decision.ClosedStage != 1 || decision.NextStage != 2 {
		t.Fatalf("decision = %+v, want stage 1 closed and stage 2 next", decision)
	}
	if !decision.Gates.Passed {
		t.Fatalf("default gates = %+v, want passed when no gates are required", decision.Gates)
	}
}

func TestCoordinatorMissingGatesBlocksPromotionButKeepsStageWake(t *testing.T) {
	coordinator := NewCoordinator(nil)
	policy := DefaultPolicy()
	policy.Mode = ModeDelivery
	policy.RequireTests = true
	policy.RequireReview = true
	policy.HumanApprovalRequired = false
	children := []Child{{ID: "a", Status: "done"}}
	decision := coordinator.EvaluateTransition(policy, children, Transition{
		ChildID:        "a",
		PreviousStatus: "in_progress",
		CurrentStatus:  "done",
	}, GateEvidence{})
	if !decision.StageClosed {
		t.Fatalf("decision = %+v, want stage wake even with missing gates", decision)
	}
	if decision.Gates.Passed || decision.CanFinalize || len(decision.Gates.Missing) != 2 {
		t.Fatalf("decision = %+v, want two missing gates and no finalization", decision)
	}
}

func TestCoordinatorFinalGatesOnlySkipsIntermediateGatesButNotFinal(t *testing.T) {
	coordinator := NewCoordinator(nil)
	policy := DefaultPolicy()
	policy.Mode = ModeDelivery
	policy.ChildCompletionSignal = CompletionSignalReviewReady
	policy.RequireTests = true
	policy.RequireReview = true
	policy.RequireMerge = true
	policy.HumanApprovalRequired = false
	policy.FinalGatesOnly = true

	intermediate := []Child{
		{ID: "a", Stage: 1, Staged: true, Status: "in_review"},
		{ID: "b", Stage: 2, Staged: true, Status: "backlog"},
	}
	decision := coordinator.EvaluateTransition(policy, intermediate, Transition{ChildID: "a", PreviousStatus: "in_progress", CurrentStatus: "in_review"}, GateEvidence{})
	if !decision.CanPromote || decision.CanFinalize {
		t.Fatalf("intermediate decision = %+v, want gate-free promotion", decision)
	}
	if !decision.Gates.Passed || len(decision.Gates.Missing) != 0 {
		t.Fatalf("intermediate gates = %+v, want final gates deferred", decision.Gates)
	}

	final := []Child{{ID: "b", Stage: 2, Staged: true, Status: "in_review"}}
	decision = coordinator.EvaluateTransition(policy, final, Transition{ChildID: "b", PreviousStatus: "in_progress", CurrentStatus: "in_review"}, GateEvidence{})
	if decision.CanFinalize || decision.Gates.Passed || len(decision.Gates.Missing) != 3 {
		t.Fatalf("final decision = %+v, want all final gates enforced", decision)
	}
}

func TestCoordinatorLegacyPolicyStillRequiresIntermediateGates(t *testing.T) {
	coordinator := NewCoordinator(nil)
	policy := DefaultPolicy()
	policy.Mode = ModeDelivery
	policy.RequireTests = true
	policy.HumanApprovalRequired = false
	children := []Child{
		{ID: "a", Stage: 1, Staged: true, Status: "done"},
		{ID: "b", Stage: 2, Staged: true, Status: "backlog"},
	}
	decision := coordinator.EvaluateTransition(policy, children, Transition{ChildID: "a", PreviousStatus: "in_progress", CurrentStatus: "done"}, GateEvidence{})
	if decision.CanPromote || decision.Gates.Passed {
		t.Fatalf("legacy decision = %+v, want intermediate gate enforcement", decision)
	}
}

func TestCoordinatorDoesNotReopenReadyChild(t *testing.T) {
	coordinator := NewCoordinator(nil)
	policy := DefaultPolicy()
	policy.ChildCompletionSignal = CompletionSignalReviewReady
	children := []Child{{ID: "a", Status: "done"}}
	decision := coordinator.EvaluateTransition(policy, children, Transition{
		ChildID:        "a",
		PreviousStatus: "in_review",
		CurrentStatus:  "done",
	}, GateEvidence{})
	if decision.CompletionOpened || decision.StageClosed {
		t.Fatalf("decision = %+v, want no duplicate completion", decision)
	}
}

func TestCoordinatorAllowsPromotionWhenRequiredEvidencePasses(t *testing.T) {
	coordinator := NewCoordinator(nil)
	policy := DefaultPolicy()
	policy.Mode = ModeDelivery
	policy.RequireTests = true
	policy.RequireMerge = true
	policy.HumanApprovalRequired = false
	children := []Child{
		{ID: "a", Stage: 1, Staged: true, Status: "done"},
		{ID: "b", Stage: 2, Staged: true, Status: "backlog"},
	}
	decision := coordinator.EvaluateTransition(policy, children, Transition{
		ChildID:        "a",
		PreviousStatus: "in_progress",
		CurrentStatus:  "done",
	}, GateEvidence{TestsPassed: true, MergeCompleted: true})
	if !decision.CanPromote || decision.CanFinalize || len(decision.Gates.Missing) != 0 {
		t.Fatalf("decision = %+v, want promotion allowed", decision)
	}
}
