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
      const ev = JSON.parse(m.data) as LiveEvent;
      // agent steps arrive many times a second and the live run card polls for them itself
      if (ev.kind !== "agent_step") onEvent(ev);
    } catch {
      // ignore malformed frames
    }
  };
  if (onError) es.onerror = onError;
  return () => es.close();
}
