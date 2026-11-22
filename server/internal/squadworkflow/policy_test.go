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

func TestPolicyLegacyJSONDisablesBlackboxBehavior(t *testing.T) {
	p, err := Parse([]byte(`{"mode":"delivery","max_active_children":1,"child_completion_signal":"review_ready","merge_mode":"integrator","require_review":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.FinalGatesOnly || p.RequirePipelineVerdict || p.ReviewAgentID != "" {
		t.Fatalf("legacy policy gained opt-in behavior: %+v", p)
	}
}

func TestPolicyMergePreservesBlackboxFields(t *testing.T) {
	base, err := Marshal(Policy{
		Mode: ModeDelivery, ChildCompletionSignal: CompletionSignalReviewReady,
		MergeMode: MergeModeIntegrator, RequireReview: true, FinalGatesOnly: true,
		ReviewAgentID:          "11111111-1111-1111-1111-111111111111",
		RequirePipelineVerdict: true,
		Pipeline:               &PipelinePolicy{MaxRounds: 3, ProductAgentID: "22222222-2222-2222-2222-222222222222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := Merge(base, []byte(`{"max_active_children":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !got.FinalGatesOnly || !got.RequirePipelineVerdict || got.ReviewAgentID == "" || got.Pipeline == nil || got.Pipeline.MaxRounds != 3 {
		t.Fatalf("merged policy lost blackbox fields: %+v", got)
	}
}

func TestPolicyValidatesBlackboxCrossFields(t *testing.T) {
	valid := Policy{
		Mode: ModeDelivery, ChildCompletionSignal: CompletionSignalReviewReady,
		MergeMode: MergeModeIntegrator, RequireReview: true,
		ReviewAgentID:          "11111111-1111-1111-1111-111111111111",
		RequirePipelineVerdict: true,
		Pipeline:               &PipelinePolicy{MaxRounds: 3, ProductAgentID: "22222222-2222-2222-2222-222222222222"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid blackbox policy: %v", err)
	}
	for name, mutate := range map[string]func(*Policy){
		"reviewer UUID":        func(p *Policy) { p.ReviewAgentID = "not-a-uuid" },
		"review gate":          func(p *Policy) { p.RequireReview = false },
		"pipeline":             func(p *Policy) { p.Pipeline = nil },
		"product agent":        func(p *Policy) { p.Pipeline.ProductAgentID = "" },
		"independent reviewer": func(p *Policy) { p.ReviewAgentID = p.Pipeline.ProductAgentID },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			pipeline := *valid.Pipeline
			p.Pipeline = &pipeline
			mutate(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("invalid policy passed: %+v", p)
			}
		})
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
