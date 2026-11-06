// SSE subscription with auto-reconnect. EventSource re-sends Last-Event-ID
// automatically, and the backend replays persisted events after it.
export interface LiveEvent {
  id: number;
  work_order_id: string;
  actor: string;
  kind: string;
  message: string;
  payload?: unknown;
}

export function subscribe(
  path: string,
  onEvent: (ev: LiveEvent) => void,
  onError?: (err: Event) => void,
): () => void {
  const es = new EventSource(path, { withCredentials: true });
  es.onmessage = (m) => {
    try {
      onEvent(JSON.parse(m.data) as LiveEvent);
    } catch {
      // ignore malformed frames
    }
  };
  // named events (backend sends event: <kind>) fall back to message handler
  const kinds = [
    "state_changed",
    "agent_started",
    "agent_completed",
    "plan_ready",
    "intake_started",
    "brief_ready",
    "plan_review_started",
    "plan_reviewed",
    "plan_gate",
    "plan_revision_requested",
    "plan_escalated",
    "converge_report",
    "converge_failed",
    "converge_append",
    "converge_gate",
    "diff_unavailable",
    "clarification_asked",
    "clarification_escalated",
    "clarification_requested",
    "clarification_answered",
    "architect_approved",
    "finding",
    "fix_requested",
    "verification_started",
    "verification_passed",
    "verification_skipped",
    "baseline_started",
    "baseline_fail",
    "baseline_pass",
    "check_pass",
    "check_fail",
    "check_unverified",
    "task_completed",
    "execution_queued",
    "readiness_ready",
    "readiness_blocked",
    "readiness_needs_verification",
    "escalated",
    "plan_approved",
    "plan_revision_human",
    "pr_approved",
    "pr_declined",
    "pr_merged",
    "pr_closed",
    "pr_track_failed",
    "revision_requested",
    "cancelled",
  ];
  for (const kind of kinds) {
    es.addEventListener(kind, (m) => {
      try {
        onEvent(JSON.parse((m as MessageEvent).data) as LiveEvent);
      } catch {
        // ignore
      }
    });
  }
  if (onError) es.onerror = onError;
  return () => es.close();
}
