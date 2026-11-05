<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import {
  ChevronLeft,
  FileText,
  BookOpen,
  Layers,
  PanelLeft,
  Plus,
  RotateCw,
  Trash2,
} from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import IconButton from "../../components/ui/IconButton.vue";
import SearchField from "../../components/ui/SearchField.vue";
import { useToast } from "../../composables/useToast";
import { money } from "../../composables/presentation";
import { useSession } from "../../stores/session";
import { useChats, type ChatAttachment } from "../../stores/chats";
import { ApiError } from "../../types/api";
import ChatComposer from "./ChatComposer.vue";
import {
  chatTitle,
  formatSize,
  groupByRecency,
  renderMarkdown,
  type PendingFile,
} from "./chatUtils";
import "./chats.css";
const store = useChats(),
  session = useSession(),
  route = useRoute(),
  router = useRouter(),
  { toast } = useToast();
const listKey = "legos-chats-list";
const query = ref(""),
  listHidden = ref(false),
  ctxOpen = ref(false),
  confirmDelete = ref(false),
  titleDraft = ref(""),
  draftModel = ref(""),
  scroller = ref<HTMLElement>(),
  composer = ref<InstanceType<typeof ChatComposer>>();
let stick = true;
const chatId = computed(() => (route.params.id as string | undefined) || "");
const chat = computed(() => store.chats.find((c) => c.id === chatId.value));
const messages = computed(() => store.messages[chatId.value] ?? []);
const busy = computed(() => !!store.busy[chatId.value]);
const model = computed({
  get: () => (chat.value ? chat.value.model : draftModel.value),
  set: (id: string) => void setModel(id),
});
const groups = computed(() => {
  const q = query.value.trim().toLowerCase();
  return groupByRecency(
    q
      ? store.chats.filter((c) => chatTitle(c).toLowerCase().includes(q))
      : store.chats,
  );
});
const usage = computed(() =>
  messages.value.reduce(
    (t, m) => ({
      tokens_in: t.tokens_in + (m.usage?.tokens_in ?? 0),
      tokens_out: t.tokens_out + (m.usage?.tokens_out ?? 0),
      cost: t.cost + (m.usage?.cost_micros ?? 0),
    }),
    { tokens_in: 0, tokens_out: 0, cost: 0 },
  ),
);
const attachments = computed(() => {
  const seen = new Set<string>();
  return messages.value
    .flatMap((m) => m.attachments ?? [])
    .filter((a) => {
      const k = a.kind + (a.kind === "kb" ? a.doc_id : a.name);
      return !seen.has(k) && !!seen.add(k);
    });
});
const lastErrorId = computed(() => {
  const last = messages.value[messages.value.length - 1];
  return last?.role === "assistant" && last.error && !busy.value ? last.id : 0;
});
const fail = (e: unknown) =>
  toast(
    e instanceof ApiError && e.code === "CHAT_BUSY"
      ? "A reply is still running in this chat. Try again in a moment."
      : e instanceof Error
        ? e.message
        : String(e),
  );
function setList(hidden: boolean) {
  listHidden.value = hidden;
  try {
    localStorage.setItem(listKey, hidden ? "1" : "0");
  } catch {
    /* Browser storage can be disabled. */
  }
}
async function newChat() {
  try {
    const c = await store.create({ model: draftModel.value });
    await router.push(`/chats/${c.id}`);
    await nextTick();
    composer.value?.focus();
  } catch (e) {
    fail(e);
  }
}
async function setModel(id: string) {
  if (!chat.value) {
    draftModel.value = id;
    return;
  }
  try {
    await store.update(chat.value.id, { model: id });
  } catch (e) {
    fail(e);
  }
}
async function rename() {
  const title = titleDraft.value.trim();
  if (!chat.value || !title || title === chat.value.title) {
    titleDraft.value = chat.value?.title ?? "";
    return;
  }
  try {
    await store.update(chat.value.id, { title });
  } catch (e) {
    titleDraft.value = chat.value.title;
    fail(e);
  }
}
async function remove() {
  if (!chat.value) return;
  try {
    await store.remove(chat.value.id);
    ctxOpen.value = confirmDelete.value = false;
    await router.replace("/chats");
    toast("Chat deleted");
  } catch (e) {
    fail(e);
  }
}
async function send(p: { content: string; files: PendingFile[] }) {
  let id = chatId.value;
  const body = {
    content: p.content,
    files: p.files.map(({ name, content }) => ({ name, content })),
  };
  try {
    if (!id) {
      id = (await store.create({ model: draftModel.value })).id;
      await router.replace(`/chats/${id}`);
    }
    stick = true;
    await store.send(id, body);
  } catch (e) {
    if (e instanceof ApiError) composer.value?.restore(p.content, p.files);
    fail(e);
  }
}
function retry() {
  const last = [...messages.value].reverse().find((m) => m.role === "user");
  const body = store.lastBody[chatId.value] ?? { content: last?.content ?? "" };
  if (body.content)
    void send({
      content: body.content,
      files: (body.files ?? []).map((f) => ({ ...f, size: f.content.length })),
    });
}
const fileLabel = (a: ChatAttachment) =>
  a.kind === "file" ? `${a.name} · ${formatSize(a.size)}` : a.name;
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
    ctxOpen.value = confirmDelete.value = false;
    stick = true;
    if (!id) return;
    try {
      await store.loadMessages(id);
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
watch(
  () => chat.value?.title,
  (t) => (titleDraft.value = t ?? ""),
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
      'in-thread': !!chatId,
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
    <div class="thread-wrap">
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
        <div class="ch-thread" aria-live="polite">
          <article
            v-for="m in messages"
            :key="m.id"
            class="ch-msg"
            :class="m.role"
          >
            <span
              v-if="m.role === 'assistant'"
              class="lg-avatar av"
              aria-hidden="true"
              >L</span
            >
            <div class="bubble">
              <p v-if="m.role === 'user'" class="plain">{{ m.content }}</p>
              <div
                v-else-if="m.content"
                class="md"
                v-html="renderMarkdown(m.content)"
              ></div>
              <span
                v-if="m.pending && !m.content"
                class="typing"
                role="status"
                aria-label="Thinking"
                ><i></i><i></i><i></i
              ></span>
              <div v-if="m.attachments?.length" class="atts">
                <template v-for="a in m.attachments" :key="a.kind + a.name">
                  <RouterLink
                    v-if="a.kind === 'kb'"
                    class="ch-chip kb"
                    to="/knowledge"
                    ><BookOpen class="icon" /><span class="ch-fn">{{
                      a.name
                    }}</span></RouterLink
                  >
                  <span v-else class="ch-chip"
                    ><FileText class="icon" /><span class="ch-fn">{{
                      a.name
                    }}</span
                    ><span class="ch-fs">{{ formatSize(a.size) }}</span></span
                  >
                </template>
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
              <small v-if="m.usage" class="ch-meta"
                >{{ m.usage.model }} ·
                {{ m.usage.tokens_in + m.usage.tokens_out }} tokens ·
                {{ money(m.usage.cost_micros) }}</small
              >
            </div>
          </article>
        </div>
      </div>
      <ChatComposer
        ref="composer"
        v-model:model="model"
        :models="store.models"
        :busy="busy"
        :readonly="!session.canWrite"
        @send="send"
        @error="toast"
      />
    </div>
    <div
      v-if="ctxOpen && chat"
      class="ctx-scrim"
      @click="ctxOpen = false"
    ></div>
    <aside v-if="chat" class="ch-ctx" aria-label="Chat details">
      <h4>Title</h4>
      <input
        v-model="titleDraft"
        class="lg-field"
        aria-label="Chat title"
        maxlength="120"
        :disabled="!session.canWrite || busy"
        @blur="rename"
        @keydown.enter.prevent="($event.target as HTMLInputElement).blur()"
      />
      <h4>Model</h4>
      <select
        class="lg-field"
        aria-label="Model"
        :value="chat.model"
        :disabled="!session.canWrite || busy"
        @change="setModel(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="m in store.models" :key="m.id" :value="m.id">
          {{ m.name }}
        </option>
      </select>
      <h4>Attachments</h4>
      <div v-if="attachments.length" class="atts">
        <template v-for="a in attachments" :key="a.kind + a.name">
          <RouterLink v-if="a.kind === 'kb'" class="ch-chip kb" to="/knowledge"
            ><BookOpen class="icon" /><span class="ch-fn">{{
              a.name
            }}</span></RouterLink
          >
          <span v-else class="ch-chip" :title="fileLabel(a)"
            ><FileText class="icon" /><span class="ch-fn">{{ a.name }}</span
            ><span class="ch-fs">{{ formatSize(a.size) }}</span></span
          >
        </template>
      </div>
      <p v-else class="empty-note">None yet.</p>
      <h4>Usage</h4>
      <dl class="ch-usage">
        <dt>Messages</dt>
        <dd>{{ messages.length }}</dd>
        <dt>Tokens in / out</dt>
        <dd>{{ usage.tokens_in }} / {{ usage.tokens_out }}</dd>
        <dt>Cost</dt>
        <dd>{{ money(usage.cost) }}</dd>
      </dl>
      <div v-if="session.canWrite" class="danger-zone">
        <Button
          v-if="!confirmDelete"
          variant="danger"
          small
          :disabled="busy"
          @click="confirmDelete = true"
          ><Trash2 class="icon" />Delete chat</Button
        >
        <template v-else>
          <span>Delete this chat and its messages?</span>
          <Button small @click="confirmDelete = false">Cancel</Button>
          <Button variant="danger" small @click="remove">Delete</Button>
        </template>
      </div>
    </aside>
  </section>
</template>
