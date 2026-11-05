import { ApiError } from "../../types/api";

export type ChatMessage = {
  id: string;
  chat_id: string;
  role: "user" | "assistant";
  content: string;
  attachments?: {
    kind: "file" | "kb";
    name: string;
    doc_id?: string;
    quote?: string;
  }[];
  error?: string;
  created_at: string;
};
export type ChatEvent =
  | { event: "user_message"; data: { message: ChatMessage } }
  | { event: "delta"; data: { text: string } }
  | { event: "done"; data: { message: ChatMessage } }
  | { event: "error"; data: { message?: ChatMessage; error: string } };
export type ChatBody = {
  content: string;
  kb?: { id: string; quote?: string };
};

export function parseFrames(buf: string): {
  events: ChatEvent[];
  rest: string;
} {
  const parts = buf.replace(/\r\n/g, "\n").split("\n\n");
  const rest = parts.pop() ?? "";
  const events: ChatEvent[] = [];
  for (const part of parts) {
    let event = "";
    const data: string[] = [];
    for (const line of part.split("\n")) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:"))
        data.push(line.slice(5).replace(/^ /, ""));
    }
    if (!event || !data.length) continue;
    try {
      events.push({ event, data: JSON.parse(data.join("\n")) } as ChatEvent);
    } catch {
      /* skip a malformed frame */
    }
  }
  return { events, rest };
}

export async function streamChat(
  chatId: string,
  body: ChatBody,
  onEvent: (e: ChatEvent) => void,
): Promise<void> {
  const resp = await fetch(
    `/api/v1/chats/${encodeURIComponent(chatId)}/messages`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Accept: "text/event-stream",
      },
      body: JSON.stringify(body),
      credentials: "same-origin",
    },
  );
  if (!resp.ok || !resp.body) {
    const text = await resp.text().catch(() => "");
    let err: { code?: string; message?: string } | undefined;
    try {
      err = JSON.parse(text)?.error;
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(
      resp.status,
      err?.code ?? "HTTP_ERROR",
      err?.message ?? (text.slice(0, 200) || `HTTP ${resp.status}`),
    );
  }
  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let terminal = false;
  for (;;) {
    const { done, value } = await reader.read();
    buf += decoder.decode(value, { stream: !done });
    const { events, rest } = parseFrames(done ? buf + "\n\n" : buf);
    buf = rest;
    for (const e of events) {
      if (e.event === "done" || e.event === "error") terminal = true;
      onEvent(e);
    }
    if (done) break;
  }
  if (!terminal) throw new Error("The reply stream ended unexpectedly");
}
