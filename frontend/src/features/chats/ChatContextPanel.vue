<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { RouterLink, useRouter } from "vue-router";
import {
  ArrowRight,
  BookOpen,
  FileText,
  GitBranch,
  Plus,
  Trash2,
} from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import Popover from "../../components/ui/Popover.vue";
import NewWorkOrderDrawer from "../factory/work-orders/NewWorkOrderDrawer.vue";
import { useToast } from "../../composables/useToast";
import {
  money,
  orderStatus,
  publishLabel,
} from "../../composables/presentation";
import { useChats, type ChatMessage, type Chat } from "../../stores/chats";
import type { RelatedDoc, WorkOrderDraft } from "../../stores/chats";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import { ApiError } from "../../types/api";
import { chatTitle, formatSize } from "./chatUtils";
const props = defineProps<{
  chat: Chat;
  messages: ChatMessage[];
  busy: boolean;
  active: boolean;
}>();
const emit = defineEmits<{ remove: [] }>();
const store = useChats(),
  factory = useFactory(),
  session = useSession(),
  router = useRouter(),
  { toast } = useToast();
const helpProject = ref(false),
  helpKnowledge = ref(false),
  confirmDelete = ref(false),
  drafting = ref(false),
  draftError = ref(""),
  saved = ref<WorkOrderDraft | null>(null),
  drawerOpen = ref(false),
  related = ref<RelatedDoc[]>([]);
let abort: AbortController | undefined;
let relatedSeq = 0;
const project = computed(() =>
  factory.projects.find((p) => p.id === props.chat.project_id),
);
const hasReply = computed(() =>
  props.messages.some(
    (m) => m.role === "assistant" && !m.pending && !m.error && m.content,
  ),
);
const blocker = computed(() =>
  !session.canWrite
    ? "You have read-only access."
    : !props.chat.project_id
      ? "Choose a project first."
      : props.busy
        ? "Wait for the reply to finish."
        : !hasReply.value
          ? "Wait for a first reply before capturing the conversation."
          : "",
);
const linked = computed(() =>
  factory.workOrders.filter((w) => w.source_chat_id === props.chat.id),
);
const attachments = computed(() => {
  const seen = new Set<string>();
  return props.messages
    .flatMap((m) => m.attachments ?? [])
    .filter((a) => {
      const k = a.kind + (a.kind === "kb" ? a.doc_id : a.name);
      return !seen.has(k) && !!seen.add(k);
    });
});
const usage = computed(() =>
  props.messages.reduce(
    (t, m) => ({
      tokens_in: t.tokens_in + (m.usage?.tokens_in ?? 0),
      tokens_out: t.tokens_out + (m.usage?.tokens_out ?? 0),
      cost: t.cost + (m.usage?.cost_micros ?? 0),
    }),
    { tokens_in: 0, tokens_out: 0, cost: 0 },
  ),
);
const reason = (e: unknown) =>
  e instanceof ApiError && e.code === "CHAT_BUSY"
    ? "A reply is still running in this chat. Try again in a moment."
    : e instanceof Error
      ? e.message
      : String(e);
async function setProject(e: Event) {
  const el = e.target as HTMLSelectElement;
  try {
    await store.update(props.chat.id, { project_id: el.value });
  } catch (err) {
    el.value = props.chat.project_id ?? "";
    toast(reason(err));
  }
}
async function createWorkOrder(fresh = false) {
  if (blocker.value || drafting.value) return;
  if (saved.value && !fresh) {
    drawerOpen.value = true;
    return;
  }
  saved.value = null;
  const ctl = (abort = new AbortController());
  drafting.value = true;
  draftError.value = "";
  try {
    const d = await store.draftWorkOrder(props.chat.id, ctl.signal);
    if (ctl.signal.aborted) return;
    saved.value = d;
    drawerOpen.value = true;
  } catch (e) {
    if (!ctl.signal.aborted) draftError.value = reason(e);
  } finally {
    if (abort === ctl) drafting.value = false;
  }
}
function cancelDraft() {
  abort?.abort();
  drafting.value = false;
}
function created(r: { id: string; planningError: string }) {
  saved.value = null;
  drawerOpen.value = false;
  void router.push({
    path: `/factory/${r.id}`,
    query: r.planningError ? { planningError: r.planningError } : {},
  });
}
async function loadRelated() {
  const seq = ++relatedSeq;
  try {
    const docs = await store.relatedKnowledge(props.chat.id);
    if (seq === relatedSeq) related.value = docs;
  } catch {
    if (seq === relatedSeq) related.value = [];
  }
}
watch(
  () => props.chat.id,
  () => {
    related.value = [];
    saved.value = null;
    drawerOpen.value = false;
    draftError.value = "";
    confirmDelete.value = false;
    cancelDraft();
  },
);
function load() {
  void loadRelated();
  if (!factory.projects.length) void factory.refreshProjects().catch(() => {});
  void factory.refreshWorkOrders().catch(() => {});
}
watch(
  () => [props.active, props.chat.id, props.busy] as const,
  ([active, id, busy], [wasActive, wasId, wasBusy]) => {
    if (active && (!wasActive || id !== wasId || (wasBusy && !busy))) load();
  },
);
onMounted(() => props.active && load());
onBeforeUnmount(() => abort?.abort());
</script>
<template>
  <div class="ctx-body">
    <section class="ctx-sec" aria-labelledby="ctx-project">
      <h4 id="ctx-project">
        Project context
        <Popover v-model:open="helpProject">
          <template #trigger="{ toggle }"
            ><button
              type="button"
              class="ctx-help"
              aria-label="What is project context?"
              aria-haspopup="true"
              :aria-expanded="helpProject"
              @click="toggle"
            >
              ?
            </button></template
          >
          <p class="ctx-explain" @click.stop>
            The project a work order from this chat will belong to. Choosing one
            does not start anything.
          </p>
        </Popover>
      </h4>
      <label class="ch-sr" for="ctx-project-select">Project</label>
      <select
        id="ctx-project-select"
        class="lg-field"
        :value="chat.project_id ?? ''"
        :disabled="!session.canWrite || busy || drafting"
        @change="setProject"
      >
        <option value="">No project</option>
        <option v-for="p in factory.projects" :key="p.id" :value="p.id">
          {{ p.name }}
        </option>
      </select>
      <p v-if="project" class="ctx-publish" data-test="publish-info">
        <GitBranch v-if="project.github_repo" class="icon" />
        <span>{{ project.github_repo || publishLabel(project) }}</span>
      </p>
    </section>
    <section class="ctx-sec" aria-labelledby="ctx-convo">
      <h4 id="ctx-convo">From this conversation</h4>
      <div class="ctx-card">
        <span class="ctx-tag">Working notes</span>
        <strong class="ctx-card-title">{{ chatTitle(chat) }}</strong>
        <p>
          Capture the useful parts. Review the scope before anything starts.
        </p>
        <Button
          v-if="!drafting"
          variant="primary"
          data-test="create-wo"
          aria-describedby="ctx-wo-note"
          :disabled="!!blocker"
          @click="createWorkOrder(false)"
          ><Plus class="icon" />Create work order</Button
        >
        <div v-else class="ctx-busy" role="status">
          <span>Drafting from the conversation…</span>
          <Button small data-test="cancel-draft" @click="cancelDraft"
            >Cancel</Button
          >
        </div>
        <p id="ctx-wo-note" class="ctx-note">
          {{ blocker || "You review and edit the draft before it is created." }}
        </p>
        <Button
          v-if="saved && !drafting"
          small
          variant="ghost"
          data-test="redraft"
          :disabled="!!blocker"
          @click="createWorkOrder(true)"
          >Draft again</Button
        >
        <p v-if="draftError" class="ctx-error" role="alert">
          {{ draftError }}
        </p>
      </div>
      <ul
        v-if="linked.length"
        class="ctx-list"
        aria-label="Work orders from this chat"
      >
        <li v-for="w in linked" :key="w.id">
          <RouterLink :to="`/factory/${w.id}`"
            ><span class="t">{{ w.title }}</span
            ><small>{{ orderStatus(w).label }}</small></RouterLink
          >
        </li>
      </ul>
    </section>
    <section v-if="related.length" class="ctx-sec" aria-labelledby="ctx-kb">
      <h4 id="ctx-kb">
        Related knowledge
        <Popover v-model:open="helpKnowledge">
          <template #trigger="{ toggle }"
            ><button
              type="button"
              class="ctx-help"
              aria-label="Why is this knowledge shown?"
              aria-haspopup="true"
              :aria-expanded="helpKnowledge"
              @click="toggle"
            >
              ?
            </button></template
          >
          <p class="ctx-explain" @click.stop>
            Notes from the knowledge base that match this conversation. They
            update after each reply.
          </p>
        </Popover>
      </h4>
      <ul class="ctx-list">
        <li v-for="d in related" :key="d.id">
          <RouterLink
            :to="{ path: '/knowledge', query: { doc: d.id } }"
            :aria-label="`Open ${d.title} in knowledge`"
            ><BookOpen class="icon" /><span class="ctx-doc"
              ><span class="t">{{ d.title }}</span
              ><small>{{ d.kind }}</small></span
            ><ArrowRight class="icon" aria-hidden="true"
          /></RouterLink>
        </li>
      </ul>
    </section>
    <section class="ctx-sec" aria-labelledby="ctx-att">
      <h4 id="ctx-att">Attachments</h4>
      <div v-if="attachments.length" class="atts">
        <template v-for="a in attachments" :key="a.kind + a.name">
          <RouterLink
            v-if="a.kind === 'kb'"
            class="ch-chip kb"
            :to="{ path: '/knowledge', query: { doc: a.doc_id } }"
            ><BookOpen class="icon" /><span class="ch-fn">{{
              a.name
            }}</span></RouterLink
          >
          <span v-else class="ch-chip"
            ><FileText class="icon" /><span class="ch-fn">{{ a.name }}</span
            ><span class="ch-fs">{{ formatSize(a.size) }}</span></span
          >
        </template>
      </div>
      <p v-else class="empty-note">None yet.</p>
    </section>
    <section class="ctx-sec" aria-labelledby="ctx-usage">
      <h4 id="ctx-usage">Usage</h4>
      <dl class="ch-usage">
        <dt>Messages</dt>
        <dd>{{ messages.length }}</dd>
        <dt>Tokens in / out</dt>
        <dd>{{ usage.tokens_in }} / {{ usage.tokens_out }}</dd>
        <dt>Cost</dt>
        <dd>{{ money(usage.cost) }}</dd>
      </dl>
    </section>
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
        <Button variant="danger" small @click="emit('remove')">Delete</Button>
      </template>
    </div>
    <NewWorkOrderDrawer
      v-if="drawerOpen && saved"
      open
      :project-i-d="chat.project_id"
      :prefill="saved"
      :source-chat-i-d="chat.id"
      @edit="saved = $event"
      @close="drawerOpen = false"
      @created="created"
    />
  </div>
</template>
