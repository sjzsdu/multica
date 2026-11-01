"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, ChevronRight, CircleAlert, Clock3, Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { issueWorkflowRunsOptions } from "@multica/core/issues/queries";
import { useWorkflowApproval } from "@multica/core/issues/mutations";
import type { SquadWorkflowRun } from "@multica/core/types";
import { useT } from "../../i18n";

function stateLabel(state: string, t: ReturnType<typeof useT<"issues">>["t"]): string {
  switch (state) {
    case "pending": return t(($) => $.detail.workflow_state_pending);
    case "blocked": return t(($) => $.detail.workflow_state_blocked);
    case "awaiting_manual": return t(($) => $.detail.workflow_state_awaiting_manual);
    case "promoted": return t(($) => $.detail.workflow_state_promoted);
    case "ready_for_finalization": return t(($) => $.detail.workflow_state_ready_for_finalization);
    case "finalized": return t(($) => $.detail.workflow_state_finalized);
    case "failed": return t(($) => $.detail.workflow_state_failed);
  }
  return state.replaceAll("_", " ");
}

function gateLabel(gate: string, t: ReturnType<typeof useT<"issues">>["t"]): string {
  return gate === "human_approval" ? t(($) => $.detail.workflow_gate_human_approval) : gate;
}

function runSummary(run: SquadWorkflowRun, t: ReturnType<typeof useT<"issues">>["t"]): string {
  if (run.state === "ready_for_finalization") return t(($) => $.detail.workflow_all_gates_passed);
  if (run.state === "finalized") return t(($) => $.detail.workflow_parent_in_review);
  if (run.last_error) return run.last_error;
  return t(($) => $.detail.workflow_stage_summary, { closed: run.closed_stage, next: run.next_stage });
}

export function WorkflowPanel({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const { data: runs = [], isLoading } = useQuery(issueWorkflowRunsOptions(issueId));
  const approval = useWorkflowApproval(issueId);
  const [open, setOpen] = useState(false);

  const latest = useMemo(() => runs[0], [runs]);
  if (isLoading || !latest) return null;

  const pendingApprovals = latest.approvals.filter((item) => !item.approved);
  const submitApproval = (gate: "review" | "human_approval", approved: boolean) => {
    approval.mutate({ runId: latest.id, gate, approved });
  };

  return (
    <div className="space-y-1">
      <button
        type="button"
        className="flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
      >
        <ChevronRight className={`size-3 shrink-0 text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`} />
        {t(($) => $.detail.workflow_section)}
        <span className="ml-auto text-[11px] font-normal text-muted-foreground">{stateLabel(latest.state, t)}</span>
      </button>
      {open && (
        <div className="space-y-2 pl-5 pr-2 text-caption">
          <div className="flex items-start gap-2 text-muted-foreground">
            {latest.state === "failed" || latest.state === "blocked" ? (
              <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" />
            ) : latest.state === "finalized" ? (
              <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-emerald-600" />
            ) : latest.state === "pending" ? (
              <Clock3 className="mt-0.5 size-3.5 shrink-0" />
            ) : (
              <Loader2 className="mt-0.5 size-3.5 shrink-0 animate-spin" />
            )}
            <span>{runSummary(latest, t)}</span>
          </div>
          {pendingApprovals.map((item) => (
            <div key={`${item.closed_stage}-${item.gate}`} className="rounded-md border border-border/70 p-2">
              <div className="mb-2 flex items-center justify-between gap-2">
                <span>{t(($) => $.detail.workflow_stage_gate, { stage: item.closed_stage, gate: gateLabel(item.gate, t) })}</span>
                <span className="text-muted-foreground">{t(($) => $.detail.workflow_needs_decision)}</span>
              </div>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  className="h-7 text-xs"
                  disabled={approval.isPending}
                  onClick={() => submitApproval(item.gate === "review" ? "review" : "human_approval", true)}
                >
                  {t(($) => $.detail.workflow_approve)}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7 text-xs"
                  disabled={approval.isPending}
                  onClick={() => submitApproval(item.gate === "review" ? "review" : "human_approval", false)}
                >
                  {t(($) => $.detail.workflow_reject)}
                </Button>
              </div>
            </div>
          ))}
          {runs.length > 1 && (
            <div className="text-muted-foreground">{t(($) => $.detail.workflow_runs_recorded, { count: runs.length })}</div>
          )}
          {approval.isError && <div className="text-destructive">{t(($) => $.detail.workflow_save_failed)}</div>}
        </div>
      )}
    </div>
  );
}
