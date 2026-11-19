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
import { ArrowRight, ClipboardList, Plus, Trash2 } from "lucide-vue-next";
import Button from "../../components/ui/Button.vue";
import Popover from "../../components/ui/Popover.vue";
import NewWorkOrderDrawer from "../factory/work-orders/NewWorkOrderDrawer.vue";
import { useToast } from "../../composables/useToast";
import { money, orderStatus } from "../../composables/presentation";
import { woPath, woPathFor } from "../projects/rail";
import { useChats, type ChatMessage, type Chat } from "../../stores/chats";
import type { WorkOrderDraft } from "../../stores/chats";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import { ApiError } from "../../types/api";
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
  changing = ref(false),
  allWorktrees = ref(false),
  confirmDelete = ref(false),
  drafting = ref(false),
  draftError = ref(""),
  saved = ref<WorkOrderDraft | null>(null),
  drawerOpen = ref(false),
  info = ref<SessionInfo | null>(null),
  infoLoading = ref(false),
  infoError = ref(""),
  now = ref(Date.now()),
  open = reactive(loadOpen());
let abort: AbortController | undefined;
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
    changing.value = false;
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
    path: woPathFor(factory.workOrders, r.id),
    query: r.planningError ? { planningError: r.planningError } : {},
  });
}
watch(
  () => props.chat.id,
  () => {
    info.value = null;
    infoError.value = "";
    infoSeq++;
    infoLoading.value = false;
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
    now.value = Date.now();
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
const projectSummary = computed(() =>
  props.chat.project_id ? "Bound" : "Not bound",
);
const WT_LIMIT = 3;
const isMine = (w: { branch: string }) =>
  !!sp.value?.local && w.branch === sp.value.local.branch;
const shownWorktrees = computed(() => {
  const list = sp.value?.worktrees ?? [];
  return allWorktrees.value ? list : list.slice(0, WT_LIMIT);
});
function wtPath(path: string) {
  const root = sp.value?.local?.path;
  if (root && path === root) return "your folder";
  return root && path.startsWith(root + "/")
    ? path.slice(root.length + 1)
    : path;
}
const crewColor = (role: string) =>
  ["foreman", "architect", "builder", "inspector", "cataloger"].includes(role)
    ? `var(--crew-${role})`
    : "var(--chart-2)";
const cacheBar = computed(() => {
  const c = ctx.value?.cache;
  const total = c ? c.read + c.write + c.uncached : 0;
  return c && total > 0
    ? { read: (c.read / total) * 100, write: (c.write / total) * 100 }
    : null;
});
function agentRole(r: string) {
  return r ? r[0].toUpperCase() + r.slice(1) : "Agent";
}
function load() {
  void loadInfo();
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
    changing.value = false;
    if (id !== wasId) allWorktrees.value = false;
    if (id !== wasId || pid === wasPid) return;
    if (info.value) info.value = { ...info.value, project: null };
    if (props.active) void loadInfo();
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
      :tone="chat.project_id ? 'ok' : 'muted'"
      :open="open.project"
      @toggle="toggle('project')"
    >
      <div v-if="project && !changing" class="proj-head">
        <span class="proj-ico" aria-hidden="true">{{
          project.name.slice(0, 1).toUpperCase()
        }}</span>
        <div class="proj-id">
          <strong>{{ project.name }}</strong>
          <span v-if="sp?.local" class="mono" :title="sp.local.path">{{
            sp.local.path
          }}</span>
        </div>
        <button
          type="button"
          class="proj-change"
          data-test="change-project"
          :disabled="!session.canWrite || busy || drafting"
          @click="changing = true"
        >
          Change
        </button>
      </div>
      <div v-else class="rail-row">
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
        <button
          v-if="changing"
          type="button"
          class="rail-more"
          @click="changing = false"
        >
          Cancel
        </button>
      </div>
      <select
        v-if="!project || changing"
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
      <p v-if="!chat.project_id" class="ctx-note">
        Not bound. This chat answers from the model, files you attach and MCP
        tools. It cannot read any repository.
      </p>
      <template v-if="sp">
        <dl
          v-if="sp.local || sp.checkout"
          class="rail-kv rail-box"
          aria-label="Project checkout"
        >
          <template v-if="sp.local">
            <dt>Your folder</dt>
            <dd class="mono" :title="sp.local.branch">
              {{ sp.local.branch || "detached" }} ·
              {{ sp.local.dirty ? `${sp.local.dirty} modified` : "clean" }}
            </dd>
          </template>
          <template v-if="sp.checkout">
            <dt>Chat reads</dt>
            <dd class="mono">checkout @ {{ shortSha(sp.checkout.sha) }}</dd>
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
          <ul class="wt-list" aria-label="Worktrees">
            <li
              v-for="w in shownWorktrees"
              :key="w.path"
              :class="{ mine: isMine(w) }"
            >
              <span class="wt-dot" aria-hidden="true"></span>
              <span class="mono wt-branch">{{ w.branch || "detached" }}</span>
              <span v-if="isMine(w)" class="wt-state">Your folder</span>
              <small class="mono" :title="w.path">{{ wtPath(w.path) }}</small>
            </li>
          </ul>
          <button
            v-if="sp.worktrees.length > WT_LIMIT"
            type="button"
            class="rail-more"
            data-test="worktrees-more"
            :aria-expanded="allWorktrees"
            @click="allWorktrees = !allWorktrees"
          >
            {{ allWorktrees ? "Show less" : `Show all ${sp.worktrees.length}` }}
          </button>
        </div>
        <div class="rail-group">
          <h5>
            Running now <span>{{ sp.running.length }}</span>
            <RouterLink
              class="rail-link"
              :to="chat.project_id ? `/projects/${chat.project_id}` : '/projects'"
              >Open factory</RouterLink
            >
          </h5>
          <ul
            v-if="sp.running.length"
            class="rail-list"
            aria-label="Running agents"
          >
            <li
              v-for="r in sp.running"
              :key="r.work_order_id + r.task_id"
              class="rail-dot"
              :style="{ '--dot': crewColor(r.role) }"
            >
              <RouterLink :to="woPathFor(factory.workOrders, r.work_order_id)">
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
          <span v-if="pct !== null" class="rail-big" data-test="ctx-pct"
            >{{ pct }}% used</span
          >
          <span v-else class="rail-big" data-test="ctx-pct"
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
          <div v-if="cacheBar" class="rail-bar thin" aria-hidden="true">
            <span
              v-if="cacheBar.read > 0"
              :style="{
                width: `${cacheBar.read}%`,
                background: 'var(--chart-1)',
              }"
            ></span>
            <span
              v-if="cacheBar.write > 0"
              :style="{
                width: `${cacheBar.write}%`,
                background: 'var(--crew-foreman)',
              }"
            ></span>
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
          <li
            v-for="m in info.mcp"
            :key="m.id"
            class="rail-mcp rail-dot"
            :class="{ off: !m.offered }"
            :style="{
              '--dot':
                m.offered && m.status === 'active'
                  ? 'var(--success)'
                  : 'var(--chart-3)',
            }"
          >
            <div class="rail-top">
              <span class="mono rail-name">{{ m.name }}</span>
              <span class="rail-pill" :class="m.offered ? 'ok' : 'muted'">{{
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
      id="task"
      title="Task"
      :summary="linked.length ? `${linked.length} from this chat` : ''"
      :open="open.task"
      @toggle="toggle('task')"
    >
      <div class="task-card">
        <div class="task-head">
          <span class="task-ico" aria-hidden="true"
            ><ClipboardList class="icon"
          /></span>
          <div>
            <strong>Turn this chat into a work order</strong>
            <p>
              A draft is written from this conversation. You review and edit it
              before anything runs.
            </p>
          </div>
        </div>
        <div v-if="drafting" class="task-busy" role="status">
          <span class="task-spin" aria-hidden="true"></span>
          <span>Drafting from the conversation…</span>
          <Button
            small
            variant="ghost"
            data-test="cancel-draft"
            @click="cancelDraft"
            >Cancel</Button
          >
        </div>
        <div v-else class="task-actions">
          <Button
            variant="primary"
            data-test="create-wo"
            aria-describedby="ctx-wo-note"
            :disabled="!!blocker"
            @click="createWorkOrder(false)"
            ><Plus class="icon" />{{
              saved ? "Open draft" : "Create work order"
            }}</Button
          >
          <Button
            v-if="saved"
            small
            variant="ghost"
            data-test="redraft"
            :disabled="!!blocker"
            @click="createWorkOrder(true)"
            >Draft again</Button
          >
        </div>
        <p v-if="blocker" id="ctx-wo-note" class="task-note">{{ blocker }}</p>
        <p v-if="draftError" class="ctx-error" role="alert">
          {{ draftError }}
        </p>
      </div>
      <ul
        v-if="linked.length"
        class="task-list"
        aria-label="Work orders from this chat"
      >
        <li v-for="w in linked" :key="w.id">
          <RouterLink :to="woPath(w.project_id, w.id)"
            ><span class="t">{{ w.title }}</span
            ><span class="rail-pill">{{ orderStatus(w).label }}</span
            ><ArrowRight class="icon" aria-hidden="true"
          /></RouterLink>
        </li>
      </ul>
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
