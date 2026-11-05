import { defineStore } from "pinia";
import { api, ApiError } from "../types/api";
import { streamChat, type ChatStreamBody } from "../services/api/chatStream";

export type ChatAttachment =
  | { kind: "file"; name: string; size: number }
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
}
export interface ChatModel {
  id: string;
  name: string;
  provider_id?: string;
  model_id?: string;
  harness?: string;
}

const TMP_USER = -1;
const TMP_REPLY = -2;
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
    async update(id: string, patch: { title?: string; model?: string }) {
      const r = await api.patch<{ chat: Chat }>(`/api/v1/chats/${id}`, patch);
      this.chats = this.chats
        .map((c) => (c.id === id ? r.chat : c))
        .sort(byActivity);
    },
    async remove(id: string) {
      await api.del(`/api/v1/chats/${id}`);
      this.chats = this.chats.filter((c) => c.id !== id);
      delete this.messages[id];
      delete this.lastBody[id];
    },
    async loadMessages(id: string) {
      const r = await api.get<{ messages?: ChatMessage[] }>(
        `/api/v1/chats/${id}/messages`,
      );
      if (!this.busy[id]) this.messages[id] = r.messages ?? [];
    },
    async send(id: string, body: ChatStreamBody) {
      const base = {
        chat_id: id,
        created_at: new Date().toISOString(),
        attachments: [],
      };
      this.messages[id] = [
        ...(this.messages[id] ?? []),
        {
          ...base,
          id: TMP_USER,
          role: "user",
          content: body.content,
          attachments: (body.files ?? []).map((f) => ({
            kind: "file" as const,
            name: f.name,
            size: new TextEncoder().encode(f.content).length,
          })),
        },
        {
          ...base,
          id: TMP_REPLY,
          role: "assistant",
          content: "",
          pending: true,
        },
      ];
      this.busy[id] = true;
      this.lastBody[id] = body;
      const swap = (tmp: number, m: ChatMessage) => {
        this.messages[id] = this.messages[id].map((x) =>
          x.id === tmp ? m : x,
        );
      };
      try {
        await streamChat(id, body, ({ event, data }) => {
          const d = data as {
            message?: ChatMessage | string;
            text?: string;
            error?: unknown;
          };
          const reply = this.messages[id].find((x) => x.id === TMP_REPLY);
          if (event === "user_message" && typeof d.message === "object")
            swap(TMP_USER, d.message);
          else if (event === "delta" && reply) reply.content += d.text ?? "";
          else if (event === "done" && typeof d.message === "object")
            swap(TMP_REPLY, d.message);
          else if (event === "error" && reply) {
            const persisted = typeof d.message === "object" ? d.message : null;
            const text =
              typeof d.error === "string"
                ? d.error
                : ((d.error as { message?: string } | undefined)?.message ??
                  persisted?.error ??
                  (typeof d.message === "string"
                    ? d.message
                    : "The reply failed."));
            if (persisted) swap(TMP_REPLY, { ...persisted, error: text });
            else Object.assign(reply, { pending: false, error: text });
          }
        });
      } catch (e) {
        if (e instanceof ApiError) {
          this.messages[id] = this.messages[id].filter((x) => x.id > 0);
          throw e;
        }
        this.busy[id] = false;
        await this.loadMessages(id).catch(() => undefined);
        throw e;
      } finally {
        this.busy[id] = false;
        void this.refresh().catch(() => undefined);
      }
    },
  },
});
