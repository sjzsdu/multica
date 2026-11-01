export interface PendingTaskIdentity {
  sessionId: string;
  taskId: string | null;
}

/**
 * Whether an authoritative pending-task refresh proves that the transcript may
 * have advanced while realtime was unavailable.
 */
export function shouldReconcilePendingTaskTransition(
  previous: PendingTaskIdentity | null,
  current: PendingTaskIdentity,
): boolean {
  return (
    previous?.sessionId === current.sessionId &&
    previous.taskId !== null &&
    previous.taskId !== current.taskId
  );
}
