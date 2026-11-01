package squadworkflow

import "testing"

func TestDefaultPolicyPreservesManualBehavior(t *testing.T) {
	p := DefaultPolicy()
	if p.Mode != ModeManual || p.ChildCompletionSignal != CompletionSignalTerminalOnly {
		t.Fatalf("default policy = %+v", p)
	}
	if p.AutoPromoteStage || p.AutoFinalizeParent {
		t.Fatalf("default policy must not perform automatic workflow writes: %+v", p)
	}
	if p.OpensCompletion("in_progress", "in_review") {
		t.Fatal("manual policy must not treat in_review as a stage completion")
	}
	if !p.OpensCompletion("in_review", "done") {
		t.Fatal("manual policy must treat done as a stage completion")
	}
}

func TestReviewReadyPolicyOpensOnInReview(t *testing.T) {
	p := DefaultPolicy()
	p.Mode = ModeStaged
	p.ChildCompletionSignal = CompletionSignalReviewReady
	if !p.OpensCompletion("in_progress", "in_review") {
		t.Fatal("review-ready policy must open on in_review")
	}
	if p.OpensCompletion("in_review", "done") {
		t.Fatal("review-ready policy must not reopen an already ready child")
	}
}

func TestMergePreservesOmittedFields(t *testing.T) {
	base, err := Marshal(Policy{
		Mode:                  ModeDelivery,
		MaxActiveChildren:     2,
		ChildCompletionSignal: CompletionSignalReviewReady,
		RequireTests:          true,
		RequireReview:         true,
		MergeMode:             MergeModeIntegrator,
		HumanApprovalRequired: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	merged, got, err := Merge(base, []byte(`{"max_active_children":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxActiveChildren != 1 || !got.RequireTests || got.MergeMode != MergeModeIntegrator {
		t.Fatalf("merged policy = %+v", got)
	}
	if len(merged) == 0 {
		t.Fatal("expected normalized policy JSON")
	}
}

func TestIsActiveChildStatus(t *testing.T) {
	for _, status := range []string{"todo", "in_progress", "in_review"} {
		if !IsActiveChildStatus(status) {
			t.Fatalf("status %q should consume an active-child slot", status)
		}
	}
	for _, status := range []string{"backlog", "blocked", "done", "cancelled"} {
		if IsActiveChildStatus(status) {
			t.Fatalf("status %q should not consume an active-child slot", status)
		}
	}
}
