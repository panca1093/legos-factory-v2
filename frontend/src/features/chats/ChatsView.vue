<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import {
  ChevronLeft,
  GitCommitHorizontal,
  Layers,
  PanelLeft,
  Plus,
  RotateCw,
} from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import IconButton from "../../components/ui/IconButton.vue";
import SearchField from "../../components/ui/SearchField.vue";
import { useToast } from "../../composables/useToast";
import { money } from "../../composables/presentation";
import { useSession } from "../../stores/session";
import { useChats } from "../../stores/chats";
import { useFactory } from "../../stores/factory";
import { ApiError, type ChatPermissionDecision } from "../../types/api";
import ChatComposer from "./ChatComposer.vue";
import AttachmentChip from "./AttachmentChip.vue";
import ChatContextPanel from "./ChatContextPanel.vue";
import ChatPermissionCard from "./ChatPermissionCard.vue";
import {
  chatTitle,
  groupByRecency,
  renderMarkdownCached,
  stepLine,
  type ChatUpload,
} from "./chatUtils";
import "./chats.css";
const store = useChats(),
  factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter(),
  { toast } = useToast();
const listKey = "legos-chats-list";
const query = ref(""),
  listHidden = ref(false),
  ctxOpen = ref(false),
  draftModel = ref(""),
  draftProject = ref(""),
  scroller = ref<HTMLElement>(),
  creating = ref(false),
  announce = ref(""),
  ctxBtn = ref<{ $el: HTMLElement }>(),
  ctxPanel = ref<HTMLElement>(),
  narrow = ref(false),
  dropping = ref(false),
  composer = ref<InstanceType<typeof ChatComposer>>();
let stick = true;
let dragDepth = 0;
const chatId = computed(() => (route.params.id as string | undefined) || "");
const chat = computed(() => store.chats.find((c) => c.id === chatId.value));
const messages = computed(() => store.messages[chatId.value] ?? []);
const busy = computed(() => !!store.busy[chatId.value]);
const draft = computed(() => !chatId.value && !!route.query.new);
const model = computed({
  get: () => (chat.value ? chat.value.model : draftModel.value),
  set: (id: string) => void setModel(id),
});
const project = computed({
  get: () => (chat.value ? (chat.value.project_id ?? "") : draftProject.value),
  set: (id: string) => {
    if (!chat.value) {
      draftProject.value = id;
      return;
    }
    store.update(chat.value.id, { project_id: id }).catch(fail);
  },
});
const groups = computed(() => {
  const q = query.value.trim().toLowerCase();
  return groupByRecency(
    q
      ? store.chats.filter((c) => chatTitle(c).toLowerCase().includes(q))
      : store.chats,
  );
});
const lastErrorId = computed(() => {
  const last = messages.value[messages.value.length - 1];
  return last?.role === "assistant" && last.error && !busy.value ? last.id : 0;
});
const projectName = (id: string) =>
  factory.projects.find((p) => p.id === id)?.name ?? id;
const fail = (e: unknown) =>
  toast(
    e instanceof ApiError && e.code === "CHAT_BUSY"
      ? "A reply is still running in this chat. Try again in a moment."
      : e instanceof Error
        ? e.message
        : String(e),
  );
const answerPermission = (reqId: string, d: ChatPermissionDecision) =>
  store.answerPermission(chatId.value, reqId, d).catch(fail);
function closeCtx() {
  ctxOpen.value = false;
  void nextTick(() => ctxBtn.value?.$el.focus());
}
watch(ctxOpen, async (open) => {
  if (!open) return;
  narrow.value = window.matchMedia("(max-width: 860px)").matches;
  await nextTick();
  [
    ...(ctxPanel.value?.querySelectorAll<HTMLElement>(
      "select:not(:disabled), button",
    ) ?? []),
  ]
    .find((el) => el.offsetParent !== null)
    ?.focus();
});
watch(busy, (now, was) => {
  if (!was || now) return;
  const last = messages.value.at(-1);
  if (last?.role === "assistant")
    announce.value = last.error
      ? "The reply failed."
      : `Reply finished. ${last.content.slice(0, 160)}`;
});
function setList(hidden: boolean) {
  listHidden.value = hidden;
  try {
    localStorage.setItem(listKey, hidden ? "1" : "0");
  } catch {
    /* Browser storage can be disabled. */
  }
}
async function newChat() {
  if (chatId.value || !draft.value)
    await router.push({ path: "/chats", query: { new: "1" } });
  await nextTick();
  composer.value?.focus();
}
async function setModel(id: string, el?: HTMLSelectElement) {
  if (!chat.value) {
    draftModel.value = id;
    return;
  }
  try {
    await store.update(chat.value.id, { model: id });
  } catch (e) {
    if (el) el.value = chat.value.model;
    fail(e);
  }
}
async function remove() {
  if (!chat.value) return;
  try {
    await store.remove(chat.value.id);
    ctxOpen.value = false;
    await router.replace("/chats");
    toast("Chat deleted");
  } catch (e) {
    fail(e);
  }
}
let pendingChat: Promise<string> | null = null;
function ensureChat() {
  if (chatId.value) return Promise.resolve(chatId.value);
  return (pendingChat ??= (async () => {
    creating.value = true;
    const from = route.fullPath;
    try {
      const id = (
        await store.create({
          model: draftModel.value,
          project_id: draftProject.value,
        })
      ).id;
      if (route.fullPath === from) await router.replace(`/chats/${id}`);
      return id;
    } finally {
      creating.value = false;
      pendingChat = null;
    }
  })());
}
async function send(p: { content: string; files: ChatUpload[] }) {
  const body = {
    content: p.content,
    ...(p.files.length ? { file_ids: p.files.map((f) => f.id!) } : {}),
  };
  const attachments = p.files.map((f) => ({
    kind: f.kind,
    name: f.name,
    size: f.size,
    file_id: f.id,
  }));
  let owner = chatId.value;
  try {
    owner = await ensureChat();
    stick = true;
    await store.send(owner, body, attachments);
  } catch (e) {
    if (e instanceof ApiError)
      composer.value?.restore(owner, p.content, p.files);
    fail(e);
  }
}
function retry() {
  const last = [...messages.value].reverse().find((m) => m.role === "user");
  const body = store.lastBody[chatId.value] ?? { content: last?.content ?? "" };
  if (!body.content) return;
  if (body.file_ids?.length) {
    composer.value?.restore(chatId.value, body.content, []);
    toast("Attach the files again to retry.");
  } else void send({ content: body.content, files: [] });
}
const canDrop = () => session.canWrite && !busy.value && !creating.value;
const hasFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes("Files");
function dragOver(e: DragEvent) {
  if (hasFiles(e)) e.preventDefault();
}
function dragEnter(e: DragEvent) {
  if (!hasFiles(e)) return;
  dragDepth++;
  if (canDrop()) dropping.value = true;
}
function dragLeave(e: DragEvent) {
  if (!hasFiles(e)) return;
  dragDepth = Math.max(0, dragDepth - 1);
  if (!dragDepth) dropping.value = false;
}
function dropFiles(e: DragEvent) {
  if (hasFiles(e)) e.preventDefault();
  dragDepth = 0;
  dropping.value = false;
  if (hasFiles(e) && canDrop())
    void composer.value?.attach([...(e.dataTransfer?.files ?? [])]);
}
function scrolled() {
  const el = scroller.value;
  if (el) stick = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
}
function toBottom() {
  void nextTick(() => {
    if (stick && scroller.value)
      scroller.value.scrollTop = scroller.value.scrollHeight;
  });
}
watch(
  chatId,
  async (id) => {
    ctxOpen.value = false;
    stick = true;
    if (!id) return;
    try {
      if (!store.busy[id] && !creating.value) await store.loadMessages(id);
      toBottom();
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        await router.replace("/chats");
        toast("That chat no longer exists");
      } else fail(e);
    }
  },
  { immediate: true },
);
watch(() => [messages.value.length, messages.value.at(-1)?.content], toBottom);
onMounted(async () => {
  try {
    listHidden.value = localStorage.getItem(listKey) === "1";
  } catch {
    listHidden.value = false;
  }
  try {
    await Promise.all([store.refresh(), store.loadModels()]);
  } catch (e) {
    fail(e);
  }
});
</script>
<template>
  <section
    class="chats"
    :class="{
      'in-thread': !!chatId || draft,
      'list-hidden': listHidden,
      'ctx-open': ctxOpen && !!chat,
    }"
    aria-label="Chats"
  >
    <aside class="chatlist" aria-label="Conversations">
      <Button
        variant="primary"
        pill
        :disabled="!session.canWrite"
        @click="newChat"
        ><Plus class="icon" />New chat</Button
      >
      <SearchField
        v-model="query"
        label="Search chats"
        placeholder="Search chats…"
      />
      <div class="ch-items">
        <template v-for="g in groups" :key="g.label">
          <div class="lg-nav-label">{{ g.label }}</div>
          <nav class="lg-nav" :aria-label="g.label">
            <RouterLink
              v-for="c in g.chats"
              :key="c.id"
              :to="`/chats/${c.id}`"
              active-class="active"
              ><span class="t">{{ chatTitle(c) }}</span></RouterLink
            >
          </nav>
        </template>
        <p v-if="!groups.length" class="empty-note">
          {{ query ? "No matching chats." : "No chats yet." }}
        </p>
      </div>
    </aside>
    <div
      class="thread-wrap"
      @dragenter="dragEnter"
      @dragover="dragOver"
      @dragleave="dragLeave"
      @drop="dropFiles"
    >
      <div class="thread-head">
        <IconButton
          class="list-btn"
          :label="
            listHidden ? 'Show conversation list' : 'Hide conversation list'
          "
          :aria-pressed="listHidden"
          @click="setList(!listHidden)"
          ><PanelLeft class="icon"
        /></IconButton>
        <IconButton
          class="back-btn"
          label="Back to chats"
          @click="router.push('/chats')"
          ><ChevronLeft class="icon"
        /></IconButton>
        <h2>{{ chatId ? chatTitle(chat) : "New chat" }}</h2>
        <IconButton
          v-if="chat"
          ref="ctxBtn"
          class="ctx-btn"
          label="Chat details"
          :aria-pressed="ctxOpen"
          @click="ctxOpen = !ctxOpen"
          ><Layers class="icon"
        /></IconButton>
      </div>
      <div ref="scroller" class="ch-scroll" @scroll="scrolled">
        <div v-if="!messages.length" class="ch-welcome">
          <h3>What should we work on?</h3>
          <p>
            Ask anything. Chats are general-purpose and separate from the
            factory.
          </p>
        </div>
        <div class="ch-thread">
          <article
            v-for="m in messages"
            :key="m.id"
            class="ch-msg"
            :class="m.role"
          >
            <span v-if="m.repo" class="ch-chip ch-repo" data-test="repo-chip"
              ><GitCommitHorizontal class="icon" /><span class="ch-fn"
                >Reading {{ projectName(m.repo.project_id) }} @
                {{ m.repo.sha.slice(0, 7) }}</span
              ></span
            >
            <div class="bubble">
              <p v-if="m.role === 'user'" class="plain">{{ m.content }}</p>
              <div
                v-else-if="m.content"
                class="md"
                v-html="renderMarkdownCached(chatId + ':' + m.id, m.content)"
              ></div>
              <span
                v-if="m.pending && !m.content"
                class="typing"
                role="status"
                aria-label="Thinking"
                ><i></i><i></i><i></i
              ></span>
              <p
                v-if="m.pending && m.step"
                class="ch-step"
                data-test="step-line"
              >
                {{ stepLine(m.step) }}
              </p>
              <ChatPermissionCard
                v-for="p in m.permissions"
                :key="p.id"
                :permission="p"
                @answer="(d) => answerPermission(p.id, d)"
              />
              <div v-if="m.attachments?.length" class="atts">
                <AttachmentChip
                  v-for="a in m.attachments"
                  :key="
                    a.kind +
                    (a.kind === 'kb' ? a.doc_id : (a.file_id ?? a.name))
                  "
                  :a="a"
                  :chat-id="chatId"
                  thumb
                />
              </div>
              <div v-if="m.error" class="ch-err" role="alert">
                {{ m.error }}
                <button
                  v-if="m.id === lastErrorId && session.canWrite"
                  type="button"
                  class="retry"
                  @click="retry"
                >
                  <RotateCw class="icon" />Retry
                </button>
              </div>
              <small
                v-if="m.usage && m.usage.tokens_in + m.usage.tokens_out > 0"
                class="ch-meta"
                >{{ m.usage.model }} ·
                {{ m.usage.tokens_in + m.usage.tokens_out }} tokens ·
                {{ money(m.usage.cost_micros) }}</small
              >
            </div>
          </article>
        </div>
      </div>
      <div v-if="dropping" class="ch-dropzone" aria-hidden="true">
        Drop files to attach
      </div>
      <ChatComposer
        ref="composer"
        :chat-id="chatId"
        :ensure-chat="ensureChat"
        :dragging="dropping"
        v-model:model="model"
        v-model:project="project"
        :models="store.models"
        :projects="factory.projects"
        :busy="busy || creating"
        :readonly="!session.canWrite"
        @send="send"
        @error="toast"
      />
    </div>
    <div role="status" aria-live="polite" class="ch-sr">{{ announce }}</div>
    <div
      v-if="ctxOpen && chat"
      class="ctx-scrim"
      @click="ctxOpen = false"
    ></div>
    <aside
      v-if="chat"
      ref="ctxPanel"
      class="ch-ctx"
      role="dialog"
      :aria-modal="narrow"
      aria-label="Chat details"
      @keydown.esc="closeCtx"
    >
      <ChatContextPanel
        :chat="chat"
        :messages="messages"
        :busy="busy"
        :active="ctxOpen"
        @remove="remove"
      />
    </aside>
  </section>
</template>
