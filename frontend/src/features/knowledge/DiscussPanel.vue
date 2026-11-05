<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { MessageSquare, Quote, Send, X } from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import IconButton from "../../components/ui/IconButton.vue";
import { api, ApiError } from "../../services/api/client";
import { streamChat, type ChatEvent } from "../../services/api/chatStream";
import type { ChatMessage } from "../../stores/chats";
import { useSession } from "../../stores/session";
import { renderMarkdown } from "./notes";

const props = defineProps<{ docId: string; title: string }>();
const quote = defineModel<string>("quote", { default: "" });
const emit = defineEmits<{ close: []; chat: [id: string] }>();
const session = useSession();

type Turn = {
  id: string;
  role: "user" | "assistant";
  text: string;
  quote?: string;
  error?: string;
};
const key = "legos-kb-chats";
const chatIds = (): Record<string, string> => {
  try {
    return JSON.parse(sessionStorage.getItem(key) ?? "{}");
  } catch {
    return {};
  }
};
const turns = ref<Turn[]>([]),
  draft = ref(""),
  busy = ref(false),
  error = ref(""),
  chatId = ref(chatIds()[props.docId] ?? "");
const body = ref<HTMLElement>(),
  input = ref<HTMLTextAreaElement>();
const starters = [
  "Summarize this note",
  "What's missing or unclear?",
  "Turn this into a checklist",
];
const canSend = computed(
  () => session.canWrite && !busy.value && !!draft.value.trim(),
);

const toTurn = (m: ChatMessage): Turn => ({
  id: String(m.id),
  role: m.role,
  text: m.content,
  quote: m.attachments?.flatMap((a) => (a.kind === "kb" ? [a.quote] : []))[0],
  error: m.error,
});
const scroll = () =>
  nextTick(() => {
    if (body.value) body.value.scrollTop = body.value.scrollHeight;
  });

async function loadHistory() {
  turns.value = [];
  error.value = "";
  chatId.value = chatIds()[props.docId] ?? "";
  if (!chatId.value) return;
  try {
    const r = await api.get<{ messages: ChatMessage[] }>(
      `/api/v1/chats/${chatId.value}/messages`,
    );
    turns.value = (r.messages ?? []).map(toTurn);
    scroll();
  } catch {
    const ids = chatIds();
    delete ids[props.docId];
    sessionStorage.setItem(key, JSON.stringify(ids));
    chatId.value = "";
  }
}
watch(() => props.docId, loadHistory);
onMounted(() => {
  loadHistory();
  input.value?.focus();
});
watch(quote, (q) => q && input.value?.focus());

async function ensureChat() {
  if (chatId.value) return chatId.value;
  const r = await api.post<{ chat: { id: string } }>("/api/v1/chats", {
    title: props.title.slice(0, 120),
  });
  chatId.value = r.chat.id;
  sessionStorage.setItem(
    key,
    JSON.stringify({ ...chatIds(), [props.docId]: r.chat.id }),
  );
  return r.chat.id;
}

async function send(text: string) {
  const content = text.trim();
  if (!content || busy.value || !session.canWrite) return;
  const q = quote.value;
  const docId = props.docId;
  busy.value = true;
  error.value = "";
  draft.value = "";
  quote.value = "";
  const mine: Turn = {
    id: `u${Date.now()}`,
    role: "user",
    text: content,
    quote: q || undefined,
  };
  const reply: Turn = { id: `a${Date.now()}`, role: "assistant", text: "" };
  turns.value.push(mine, reply);
  scroll();
  const onEvent = (e: ChatEvent) => {
    if (docId !== props.docId) return;
    const r = turns.value.find((t) => t.id === reply.id);
    if (!r) return;
    if (e.event === "delta") r.text += e.data.text;
    else if (e.event === "done") {
      r.text = e.data.message.content;
      r.id = String(e.data.message.id);
    } else if (e.event === "error") {
      r.error = e.data.error;
    }
    scroll();
  };
  try {
    const id = await ensureChat();
    await streamChat(
      id,
      { content, kb: { id: docId, ...(q ? { quote: q } : {}) } },
      onEvent,
    );
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    if (e instanceof ApiError) {
      turns.value = turns.value.filter((t) => t !== mine && t !== reply);
      draft.value = content;
      quote.value = q;
    } else reply.error = error.value;
  } finally {
    busy.value = false;
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    send(draft.value);
  }
}
</script>
<template>
  <aside class="nb-talk" aria-label="Discussion">
    <div class="talk-h">
      <div>
        <b>Discuss</b>
        <div class="small muted">{{ title }}</div>
      </div>
      <IconButton label="Close discussion" @click="emit('close')"
        ><X class="icon"
      /></IconButton>
    </div>
    <div ref="body" class="talk-body" aria-live="polite">
      <div v-if="!turns.length" class="talk-empty">
        <MessageSquare class="icon" />
        <p>Ask about this note. The assistant reads the whole note first.</p>
        <button
          v-for="s in starters"
          :key="s"
          type="button"
          class="chip"
          :disabled="!session.canWrite || busy"
          @click="send(s)"
        >
          {{ s }}
        </button>
      </div>
      <div
        v-for="t in turns"
        :key="t.id"
        class="km"
        :class="t.role === 'user' ? 'user' : 'ai'"
        data-test="kb-turn"
      >
        <template v-if="t.role === 'user'">
          <div v-if="t.quote" class="kq">“{{ t.quote.slice(0, 120) }}”</div>
          {{ t.text }}
        </template>
        <template v-else>
          <!-- eslint-disable-next-line vue/no-v-html -- escape-then-format renderer -->
          <div v-if="t.text" class="prose" v-html="renderMarkdown(t.text)" />
          <div v-else-if="!t.error" class="typing" aria-label="Thinking">
            <i></i><i></i><i></i>
          </div>
          <div v-if="t.error" class="err" role="alert">{{ t.error }}</div>
        </template>
      </div>
    </div>
    <div class="talk-in">
      <p v-if="error" class="notice" role="alert">{{ error }}</p>
      <p v-else-if="!session.canWrite" class="small muted">
        Viewers can read notes but not start chats.
      </p>
      <div v-if="quote" class="quote">
        <Quote class="icon" />
        <span
          >“{{ quote.slice(0, 140) }}{{ quote.length > 140 ? "…" : "" }}”</span
        >
        <button
          type="button"
          class="x"
          aria-label="Remove quote"
          @click="quote = ''"
        >
          <X class="icon" />
        </button>
      </div>
      <div class="composer">
        <textarea
          ref="input"
          v-model="draft"
          rows="1"
          :disabled="!session.canWrite"
          placeholder="Ask about this note…"
          aria-label="Ask about this note"
          @keydown="onKey"
        />
        <div class="bar">
          <Button
            variant="ghost"
            small
            :disabled="!chatId"
            data-test="kb-to-chat"
            @click="emit('chat', chatId)"
            ><MessageSquare class="icon" />Open in Chats</Button
          >
          <button
            type="button"
            class="send"
            :disabled="!canSend"
            aria-label="Send"
            data-test="kb-send"
            @click="send(draft)"
          >
            <Send class="icon" />
          </button>
        </div>
      </div>
    </div>
  </aside>
</template>
