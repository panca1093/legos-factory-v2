import { ApiError } from "../../types/api";
import type { ChatMessage, ChatRepo, ChatStep } from "../../stores/chats";

export interface ChatStreamBody {
  content: string;
  files?: { name: string; content: string; encoding?: "base64" }[];
  kb?: { id: string; quote?: string };
}
export type ChatBody = ChatStreamBody;
export interface ChatStreamEvent {
  event: string;
  data: unknown;
}
export type ChatEvent =
  | { event: "user_message"; data: { message: ChatMessage } }
  | { event: "repo"; data: ChatRepo }
  | { event: "delta"; data: { text: string } }
  | { event: "step"; data: { tools: ChatStep[] } }
  | { event: "title"; data: { title: string } }
  | { event: "done"; data: { message: ChatMessage } }
  | { event: "error"; data: { message?: ChatMessage; error: string } };

export function parseFrames(buf: string) {
  const events: ChatStreamEvent[] = [];
  const p = createFrameParser((e) => events.push(e));
  p.push(buf);
  return { events: events as ChatEvent[], rest: p.rest() };
}

export function createFrameParser(onEvent: (e: ChatStreamEvent) => void) {
  let buf = "";
  function frame(raw: string) {
    let event = "message";
    const data: string[] = [];
    for (const line of raw.split("\n")) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:"))
        data.push(line.slice(5).replace(/^ /, ""));
    }
    if (!data.length) return;
    const text = data.join("\n");
    let parsed: unknown = text;
    try {
      parsed = JSON.parse(text);
    } catch {
      /* non-JSON payload is passed through as text */
    }
    onEvent({ event, data: parsed });
  }
  return {
    push(chunk: string) {
      buf += chunk;
      const hold = buf.endsWith("\r") ? "\r" : "";
      buf =
        buf.slice(0, buf.length - hold.length).replace(/\r\n?/g, "\n") + hold;
      let i: number;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        frame(buf.slice(0, i));
        buf = buf.slice(i + 2);
      }
    },
    rest: () => buf,
    flush() {
      buf = buf.replace(/\r\n?/g, "\n");
      if (buf.trim()) frame(buf);
      buf = "";
    },
  };
}

export async function streamChat(
  chatId: string,
  body: ChatStreamBody,
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
  if (!resp.ok) {
    if (resp.status === 401)
      window.dispatchEvent(new Event("factory:unauthorized"));
    const text = await resp.text();
    let err: { code?: string; message?: string } | undefined;
    try {
      err = JSON.parse(text)?.error;
    } catch {
      /* body was not JSON */
    }
    throw new ApiError(
      resp.status,
      err?.code ?? "HTTP_ERROR",
      err?.message ?? text.slice(0, 200),
    );
  }
  if (!resp.body) throw new Error("Streaming is not supported by this browser");
  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let terminal = false;
  const parser = createFrameParser((e) => {
    if (e.event === "done" || e.event === "error") terminal = true;
    onEvent(e as ChatEvent);
  });
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      parser.push(decoder.decode(value, { stream: true }));
    }
    parser.push(decoder.decode());
    parser.flush();
    if (!terminal) throw new Error("The reply stream ended unexpectedly");
  } finally {
    reader.cancel().catch(() => undefined);
  }
}
