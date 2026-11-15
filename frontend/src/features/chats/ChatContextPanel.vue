<script setup lang="ts">
import {
  computed,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  watch,
} from "vue";
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
import RailSection from "./RailSection.vue";
import {
  cacheState,
  callsLine,
  fmtTokens,
  loadOpen,
  partBars,
  partColors,
  saveOpen,
  shortSha,
  sinceLabel,
  usedPct,
  type SessionInfo,
} from "./sessionRail";
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
  related = ref<RelatedDoc[]>([]),
  info = ref<SessionInfo | null>(null),
  infoLoading = ref(false),
  infoError = ref(""),
  now = ref(Date.now()),
  open = reactive(loadOpen());
let abort: AbortController | undefined;
let relatedSeq = 0;
let infoSeq = 0;
let tick: ReturnType<typeof setInterval> | undefined;
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
    info.value = null;
    infoError.value = "";
    saved.value = null;
    drawerOpen.value = false;
    draftError.value = "";
    confirmDelete.value = false;
    cancelDraft();
  },
);
async function loadInfo() {
  const seq = ++infoSeq;
  infoLoading.value = true;
  try {
    const r = await store.sessionInfo(props.chat.id);
    if (seq !== infoSeq) return;
    info.value = r;
    infoError.value = "";
  } catch (e) {
    if (seq === infoSeq) infoError.value = reason(e);
  } finally {
    if (seq === infoSeq) infoLoading.value = false;
  }
}
function toggle(key: string) {
  open[key] = !open[key];
  saveOpen({ ...open });
}
const ctx = computed(() => info.value?.context);
const bars = computed(() =>
  ctx.value ? partBars(ctx.value.parts, ctx.value.used, ctx.value.window) : [],
);
const pct = computed(() =>
  ctx.value ? usedPct(ctx.value.used, ctx.value.window) : null,
);
const cache = computed(() => cacheState(ctx.value?.cache, now.value));
const freeTokens = computed(() =>
  ctx.value && ctx.value.window > 0
    ? Math.max(0, ctx.value.window - ctx.value.used)
    : 0,
);
const ctxSummary = computed(() =>
  !ctx.value
    ? ""
    : `${pct.value === null ? fmtTokens(ctx.value.used) : `${pct.value}%`}${cache.value ? ` · cache ${cache.value.warm ? "warm" : "cold"}` : ""}`,
);
const barLabel = computed(() =>
  !ctx.value
    ? ""
    : ctx.value.window > 0
      ? `Context ${fmtTokens(ctx.value.used)} of ${fmtTokens(ctx.value.window)} tokens used`
      : `Context ${fmtTokens(ctx.value.used)} tokens used`,
);
const offered = computed(() => info.value?.mcp.filter((m) => m.offered) ?? []);
const sp = computed(() => info.value?.project ?? null);
const projectSummary = computed(() => sp.value?.local?.branch ?? "");
function agentRole(r: string) {
  return r ? r[0].toUpperCase() + r.slice(1) : "Agent";
}
function load() {
  void loadInfo();
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
watch(
  () => [props.chat.id, props.chat.project_id] as const,
  ([id, pid], [wasId, wasPid]) => {
    if (props.active && id === wasId && pid !== wasPid) void loadInfo();
  },
);
onMounted(() => {
  if (props.active) load();
  tick = setInterval(() => (now.value = Date.now()), 30000);
});
onBeforeUnmount(() => {
  abort?.abort();
  clearInterval(tick);
});
</script>
<template>
  <div class="ctx-body">
    <RailSection
      id="project"
      title="Project"
      :summary="projectSummary"
      :open="open.project"
      @toggle="toggle('project')"
    >
      <div class="rail-row">
        <label class="rail-lbl" for="ctx-project-select">Project</label>
        <Popover v-model:open="helpProject">
          <template #trigger="{ toggle: pop }"
            ><button
              type="button"
              class="ctx-help"
              aria-label="What is project context?"
              aria-haspopup="true"
              :aria-expanded="helpProject"
              @click="pop"
            >
              ?
            </button></template
          >
          <p class="ctx-explain" @click.stop>
            The project a work order from this chat will belong to. It also lets
            the chat read the repository. Binding starts a fresh agent session;
            the history is carried over.
          </p>
        </Popover>
      </div>
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
      <p v-if="!chat.project_id" class="ctx-note">
        Not bound. This chat answers from the model, files you attach and MCP
        tools. It cannot read any repository.
      </p>
      <template v-if="sp">
        <p v-if="sp.local" class="mono rail-path" :title="sp.local.path">
          {{ sp.local.path }}
        </p>
        <dl class="rail-kv" aria-label="Project checkout">
          <template v-if="sp.local">
            <dt>Your folder</dt>
            <dd class="mono">
              {{ sp.local.branch || "detached" }} ·
              {{ sp.local.dirty ? `${sp.local.dirty} modified` : "clean" }}
            </dd>
          </template>
          <template v-if="sp.checkout">
            <dt>Chat reads</dt>
            <dd class="mono">
              factory checkout @ {{ shortSha(sp.checkout.sha) }}
            </dd>
            <template v-if="sp.local">
              <dt>Behind your folder</dt>
              <dd>
                {{
                  sp.checkout.behind_local === null
                    ? "unknown"
                    : `${sp.checkout.behind_local} commit${sp.checkout.behind_local === 1 ? "" : "s"}`
                }}
              </dd>
            </template>
          </template>
        </dl>
        <div v-if="sp.worktrees.length" class="rail-group">
          <h5>
            Worktrees <span>{{ sp.worktrees.length }}</span>
          </h5>
          <ul class="rail-list" aria-label="Worktrees">
            <li v-for="w in sp.worktrees" :key="w.path">
              <span class="mono rail-name">{{ w.branch || "detached" }}</span>
              <span
                v-if="sp.local && w.branch === sp.local.branch"
                class="rail-pill"
                >Your folder</span
              >
              <small class="mono" :title="w.path">{{ w.path }}</small>
            </li>
          </ul>
        </div>
        <div class="rail-group">
          <h5>
            Running now <span>{{ sp.running.length }}</span>
            <RouterLink class="rail-link" to="/factory"
              >Open factory</RouterLink
            >
          </h5>
          <ul
            v-if="sp.running.length"
            class="rail-list"
            aria-label="Running agents"
          >
            <li v-for="r in sp.running" :key="r.work_order_id + r.task_id">
              <RouterLink :to="`/factory/${r.work_order_id}`">
                <span class="rail-name"
                  >{{ agentRole(r.role) }} · {{ r.state }}</span
                >
                <span class="rail-pill">{{ sinceLabel(r.since, now) }}</span>
                <small>{{ r.title }}</small>
                <small class="mono" :title="r.workspace"
                  >workspace {{ r.workspace }}</small
                >
              </RouterLink>
            </li>
          </ul>
          <p v-else class="empty-note">No agents running.</p>
        </div>
      </template>
    </RailSection>
    <RailSection
      id="context"
      title="Context"
      :summary="ctxSummary"
      :open="open.context"
      @toggle="toggle('context')"
    >
      <p v-if="!info && infoLoading" class="ctx-note" role="status">Loading…</p>
      <p v-else-if="!info && infoError" class="ctx-error" role="alert">
        {{ infoError }}
      </p>
      <template v-else-if="info && ctx">
        <p v-if="infoError" class="ctx-error" role="alert">
          Could not refresh: {{ infoError }}
        </p>
        <div class="rail-top">
          <span v-if="pct !== null" data-test="ctx-pct">{{ pct }}% used</span>
          <span v-else data-test="ctx-pct"
            >{{ fmtTokens(ctx.used) }} tokens</span
          >
          <span class="mono" data-test="ctx-used">
            {{ fmtTokens(ctx.used)
            }}<template v-if="ctx.window > 0">
              / {{ fmtTokens(ctx.window) }}</template
            >
          </span>
        </div>
        <div
          v-if="bars.length"
          class="rail-bar"
          role="img"
          :aria-label="barLabel"
        >
          <span
            v-for="b in bars"
            :key="b.key"
            :style="{ width: `${b.width}%`, background: partColors[b.key] }"
          ></span>
        </div>
        <template v-if="bars.length">
          <ul class="rail-legend" aria-label="Context breakdown, estimated">
            <li v-for="b in bars" :key="b.key">
              <span
                class="rail-sw"
                :style="{ background: partColors[b.key] }"
                aria-hidden="true"
              ></span>
              <span class="rail-lab">{{ b.label }}</span>
              <span class="mono">{{ fmtTokens(b.tokens) }}</span>
              <span class="mono">{{ ctx.window > 0 ? `${b.pct}%` : "" }}</span>
            </li>
            <li v-if="ctx.window > 0">
              <span class="rail-sw free" aria-hidden="true"></span>
              <span class="rail-lab">Free</span>
              <span class="mono">{{ fmtTokens(freeTokens) }}</span>
              <span class="mono">{{ 100 - (pct ?? 0) }}%</span>
            </li>
          </ul>
          <p class="ctx-note">
            Split is estimated from byte counts, not measured by the model.
          </p>
        </template>
        <p v-else class="ctx-note">
          No breakdown for this chat yet. Used tokens are from the last reply.
        </p>
        <div class="rail-cache" data-test="cache-card">
          <div class="rail-top">
            <span>Prompt cache</span>
            <span class="rail-pill" :class="cache?.warm ? 'warm' : 'cold'">{{
              cache?.warm ? `Warm · ${cache.minutesLeft} min left` : "Cold"
            }}</span>
          </div>
          <dl v-if="ctx.cache" class="rail-kv">
            <dt>Read from cache</dt>
            <dd class="mono">
              {{ fmtTokens(ctx.cache.read) }} · {{ ctx.cache.hit_pct }}%
            </dd>
            <dt>Written to cache</dt>
            <dd class="mono">{{ fmtTokens(ctx.cache.write) }}</dd>
            <dt>Uncached input</dt>
            <dd class="mono">{{ fmtTokens(ctx.cache.uncached) }}</dd>
          </dl>
          <p v-else class="ctx-note">No reply yet.</p>
        </div>
        <dl class="rail-kv">
          <dt>This chat</dt>
          <dd>
            {{ info.totals.turns }} turn{{ info.totals.turns === 1 ? "" : "s" }}
            ·
            {{ fmtTokens(info.totals.tokens_in + info.totals.tokens_out) }}
            tokens · {{ money(info.totals.cost_micros) }}
          </dd>
        </dl>
      </template>
    </RailSection>
    <RailSection
      id="mcp"
      title="MCP"
      :summary="info ? `${offered.length} offered` : ''"
      :open="open.mcp"
      @toggle="toggle('mcp')"
    >
      <p v-if="!info && infoLoading" class="ctx-note" role="status">Loading…</p>
      <p v-else-if="!info && infoError" class="ctx-error" role="alert">
        {{ infoError }}
      </p>
      <template v-else-if="info">
        <ul v-if="info.mcp.length" class="rail-list" aria-label="MCP servers">
          <li v-for="m in info.mcp" :key="m.id" class="rail-mcp">
            <div class="rail-top">
              <span class="mono rail-name">{{ m.name }}</span>
              <span class="rail-pill" :class="m.offered ? 'warm' : 'cold'">{{
                m.offered ? "Offered to chat" : "Not offered to chat"
              }}</span>
            </div>
            <dl class="rail-kv">
              <dt>Server</dt>
              <dd>{{ m.status }} · {{ m.transport }} · {{ m.auth }}</dd>
              <dt>Tools</dt>
              <dd>
                {{ m.tools_enabled }} of {{ m.tools_total }} on<template
                  v-if="m.offered && m.asks"
                >
                  · asks before each call</template
                >
              </dd>
              <template v-if="callsLine(m.calls)">
                <dt>Used in this chat</dt>
                <dd class="mono">{{ callsLine(m.calls) }}</dd>
              </template>
            </dl>
          </li>
        </ul>
        <p v-else class="empty-note">No MCP servers.</p>
        <RouterLink class="rail-link" to="/settings"
          >Manage in Settings</RouterLink
        >
      </template>
    </RailSection>
    <RailSection
      id="convo"
      title="From this conversation"
      :open="open.convo"
      @toggle="toggle('convo')"
    >
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
      <div v-if="related.length" class="rail-group">
        <h5>
          Related knowledge
          <Popover v-model:open="helpKnowledge">
            <template #trigger="{ toggle: pop }"
              ><button
                type="button"
                class="ctx-help"
                aria-label="Why is this knowledge shown?"
                aria-haspopup="true"
                :aria-expanded="helpKnowledge"
                @click="pop"
              >
                ?
              </button></template
            >
            <p class="ctx-explain" @click.stop>
              Notes from the knowledge base that match this conversation. They
              update after each reply.
            </p>
          </Popover>
        </h5>
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
      </div>
      <div class="rail-group">
        <h5>Attachments</h5>
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
      </div>
      <div class="rail-group">
        <h5>Usage</h5>
        <dl class="ch-usage">
          <dt>Messages</dt>
          <dd>{{ messages.length }}</dd>
          <dt>Tokens in / out</dt>
          <dd>{{ usage.tokens_in }} / {{ usage.tokens_out }}</dd>
          <dt>Cost</dt>
          <dd>{{ money(usage.cost) }}</dd>
        </dl>
      </div>
    </RailSection>
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
