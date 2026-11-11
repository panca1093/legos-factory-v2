import { defineStore } from "pinia";
import { api, ApiError } from "../types/api";
import { base64Size } from "../features/chats/chatUtils";
import { streamChat, type ChatStreamBody } from "../services/api/chatStream";

export type ChatAttachment =
  | { kind: "file"; name: string; size: number }
  | { kind: "image"; name: string; size: number }
  | { kind: "kb"; name: string; doc_id: string; quote?: string };
export interface ChatUsage {
  model: string;
  tokens_in: number;
  tokens_out: number;
  cost_micros: number;
}
export interface Chat {
  id: string;
  user_id: string;
  project_id?: string;
  title: string;
  model: string;
  created_at: string;
  updated_at: string;
}
export interface ChatRepo {
  project_id: string;
  sha: string;
}
export interface ChatStep {
  name: string;
  target: string;
  failed: boolean;
}
export interface ChatMessage {
  id: number;
  chat_id: string;
  role: "user" | "assistant";
  content: string;
  attachments: ChatAttachment[];
  usage?: ChatUsage;
  error?: string;
  created_at: string;
  pending?: boolean;
  // set in this browser only, while or after a turn that read the project's code
  repo?: ChatRepo;
  step?: ChatStep;
}
export interface WorkOrderDraft {
  title: string;
  objective: string;
  acceptance: string;
}
export interface RelatedDoc {
  id: string;
  title: string;
  kind: string;
  snippet?: string;
}
export interface ChatModel {
  id: string;
  name: string;
  provider_id?: string;
  model_id?: string;
  harness?: string;
}

let tmpId = 0;
const epoch = new Map<string, number>();
const bump = (id: string) => {
  const n = (epoch.get(id) ?? 0) + 1;
  epoch.set(id, n);
  return n;
};
const byActivity = (a: Chat, b: Chat) =>
  b.updated_at.localeCompare(a.updated_at);

export const useChats = defineStore("chats", {
  state: () => ({
    chats: [] as Chat[],
    models: [] as ChatModel[],
    defaultModel: "",
    messages: {} as Record<string, ChatMessage[]>,
    busy: {} as Record<string, boolean>,
    lastBody: {} as Record<string, ChatStreamBody>,
  }),
  actions: {
    async refresh() {
      const r = await api.get<{ chats?: Chat[] }>("/api/v1/chats");
      this.chats = r.chats ?? [];
    },
    async loadModels() {
      if (this.models.length) return;
      const r = await api.get<{ default?: string; models?: ChatModel[] }>(
        "/api/v1/chat/models",
      );
      this.models = r.models ?? [];
      this.defaultModel = r.default ?? "";
    },
    async create(body: { title?: string; model?: string } = {}) {
      const r = await api.post<{ chat: Chat }>("/api/v1/chats", body);
      this.chats = [r.chat, ...this.chats.filter((c) => c.id !== r.chat.id)];
      return r.chat;
    },
    async update(id: string, patch: { model?: string; project_id?: string }) {
      const r = await api.patch<{ chat: Chat }>(`/api/v1/chats/${id}`, patch);
      this.chats = this.chats
        .map((c) => (c.id === id ? r.chat : c))
        .sort(byActivity);
    },
    async draftWorkOrder(id: string, signal?: AbortSignal) {
      return api.post<WorkOrderDraft>(
        `/api/v1/chats/${id}/work-order-draft`,
        {},
        signal,
      );
    },
    async relatedKnowledge(id: string) {
      const r = await api.get<{ docs?: RelatedDoc[] }>(
        `/api/v1/chats/${id}/related-knowledge`,
      );
      return r.docs ?? [];
    },
    async remove(id: string) {
      await api.del(`/api/v1/chats/${id}`);
      this.chats = this.chats.filter((c) => c.id !== id);
      delete this.messages[id];
      delete this.lastBody[id];
    },
    async loadMessages(id: string) {
      const token = bump(id);
      const r = await api.get<{ messages?: ChatMessage[] }>(
        `/api/v1/chats/${id}/messages`,
      );
      if (token === epoch.get(id) && !this.busy[id])
        this.messages[id] = r.messages ?? [];
    },
    async send(id: string, body: ChatStreamBody) {
      const tmpUser = --tmpId;
      const tmpReply = --tmpId;
      const base = {
        chat_id: id,
        created_at: new Date().toISOString(),
        attachments: [],
      };
      bump(id);
      this.messages[id] = [
        ...(this.messages[id] ?? []),
        {
          ...base,
          id: tmpUser,
          role: "user",
          content: body.content,
          attachments: (body.files ?? []).map((f) =>
            f.encoding === "base64"
              ? {
                  kind: "image" as const,
                  name: f.name,
                  size: base64Size(f.content),
                }
              : {
                  kind: "file" as const,
                  name: f.name,
                  size: new TextEncoder().encode(f.content).length,
                },
          ),
        },
        {
          ...base,
          id: tmpReply,
          role: "assistant",
          content: "",
          pending: true,
        },
      ];
      this.busy[id] = true;
      this.lastBody[id] = body;
      const find = (tmp: number) => this.messages[id].find((x) => x.id === tmp);
      const markFailed = (tmp: number, text: string) => {
        const m = find(tmp);
        if (m) Object.assign(m, { pending: false, error: m.error ?? text });
      };
      const swap = (tmp: number, m: ChatMessage, error?: string) => {
        if (!(m.id > 0))
          return markFailed(tmp, "The server sent an invalid message.");
        this.messages[id] = this.messages[id].map((x) =>
          x.id === tmp
            ? {
                ...m,
                ...(x.repo ? { repo: x.repo } : {}),
                ...(error ? { error } : {}),
              }
            : x,
        );
      };
      const interrupted = async (text: string) => {
        markFailed(tmpReply, text);
        const partial = find(tmpReply);
        this.busy[id] = false;
        await this.loadMessages(id).catch(() => undefined);
        if (
          partial &&
          !find(tmpReply) &&
          this.messages[id].at(-1)?.role !== "assistant"
        )
          this.messages[id] = [...this.messages[id], partial];
      };
      try {
        await streamChat(id, body, ({ event, data }) => {
          const d = data as unknown as {
            message?: ChatMessage | string;
            text?: string;
            title?: string;
            error?: unknown;
            project_id?: string;
            sha?: string;
            tools?: ChatStep[];
          };
          const reply = find(tmpReply);
          if (event === "user_message" && typeof d.message === "object")
            swap(tmpUser, d.message);
          else if (event === "delta" && reply) {
            reply.content += d.text ?? "";
            delete reply.step;
          } else if (event === "repo" && reply && d.project_id && d.sha)
            reply.repo = { project_id: d.project_id, sha: d.sha };
          else if (event === "step" && reply && d.tools?.length)
            reply.step = d.tools[d.tools.length - 1];
          else if (event === "title" && d.title)
            this.chats = this.chats.map((c) =>
              c.id === id ? { ...c, title: d.title as string } : c,
            );
          else if (event === "done") {
            if (typeof d.message === "object") swap(tmpReply, d.message);
            else markFailed(tmpReply, "The server sent an invalid message.");
          } else if (event === "error") {
            const persisted = typeof d.message === "object" ? d.message : null;
            const text =
              typeof d.error === "string"
                ? d.error
                : ((d.error as { message?: string } | undefined)?.message ??
                  persisted?.error ??
                  (typeof d.message === "string"
                    ? d.message
                    : "The reply failed."));
            if (persisted) swap(tmpReply, persisted, text);
            else markFailed(tmpReply, text);
          }
        });
      } catch (e) {
        if (e instanceof ApiError) {
          this.messages[id] = this.messages[id].filter(
            (x) => x.id !== tmpUser && x.id !== tmpReply,
          );
          throw e;
        }
        await interrupted(
          e instanceof Error && e.message
            ? e.message
            : "The connection was lost before the reply finished.",
        );
        throw e;
      } finally {
        this.busy[id] = false;
        void this.refresh().catch(() => undefined);
      }
    },
  },
});
