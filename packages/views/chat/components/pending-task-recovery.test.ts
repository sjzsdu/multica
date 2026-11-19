import { describe, expect, it } from "vitest";

import { shouldReconcilePendingTaskTransition } from "./pending-task-recovery";

describe("shouldReconcilePendingTaskTransition", () => {
  it("reconciles when a pending task completes", () => {
    expect(shouldReconcilePendingTaskTransition(
      { sessionId: "session-1", taskId: "task-1" },
      { sessionId: "session-1", taskId: null },
    )).toBe(true);
  });

  it("reconciles when a queued follow-up becomes the new head", () => {
    expect(shouldReconcilePendingTaskTransition(
      { sessionId: "session-1", taskId: "task-1" },
      { sessionId: "session-1", taskId: "task-2" },
    )).toBe(true);
  });

  it("does not reconcile an unchanged task, first observation, or session switch", () => {
    expect(shouldReconcilePendingTaskTransition(
      { sessionId: "session-1", taskId: "task-1" },
      { sessionId: "session-1", taskId: "task-1" },
    )).toBe(false);
    expect(shouldReconcilePendingTaskTransition(
      null,
      { sessionId: "session-1", taskId: "task-1" },
    )).toBe(false);
    expect(shouldReconcilePendingTaskTransition(
      { sessionId: "session-1", taskId: "task-1" },
      { sessionId: "session-2", taskId: null },
    )).toBe(false);
  });
});
