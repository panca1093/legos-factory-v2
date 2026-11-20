<script setup lang="ts">
import { toneOf } from "./tone";
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { Copy, MoreHorizontal } from "lucide-vue-next";
import Popover from "../../components/ui/Popover.vue";
import {
  api,
  type AgentAccount,
  type AgentInfo,
  type ApiModels,
  type ApiProvider,
  type NewAccount,
} from "../../types/api";
import { resetsIn } from "../../composables/presentation";

const props = defineProps<{
  agent: AgentInfo;
  canWrite: boolean;
  credentialsConfigured: boolean;
  week?: Record<string, { tokens: number; runs: number }>;
}>();
const emit = defineEmits<{ changed: []; error: [string] }>();

const base = computed(() => `/api/v1/agents/${props.agent.id}`);
const active = computed(() => props.agent.accounts.find((a) => a.active));
const busy = ref(false);
const menu = ref(false);

const genericLogin: Record<string, string> = {
  claude: "claude auth login",
  codex: "codex login --device-auth",
  opencode: "opencode auth login --standalone",
};
const adding = ref(false),
  label = ref("");
const isClaude = computed(() => props.agent.id === "claude");
const presets: Record<
  ApiProvider,
  { name: string; base_url: string; models: Required<ApiModels> }
> = {
  deepseek: {
    name: "DeepSeek",
    base_url: "https://api.deepseek.com/anthropic",
    models: {
      opus: "deepseek-flash[1m]",
      sonnet: "deepseek-v4-pro",
      haiku: "deepseek-chat",
    },
  },
  glm: {
    name: "GLM (Z.ai)",
    base_url: "https://api.z.ai/api/anthropic",
    models: {
      opus: "glm-5.3[1m]",
      sonnet: "glm-5.3[1m]",
      haiku: "glm-5.3-flash[1m]",
    },
  },
  custom: {
    name: "Custom",
    base_url: "",
    models: { opus: "", sonnet: "", haiku: "" },
  },
};
const modelSlots = ["opus", "sonnet", "haiku"] as const;
const authMode = ref<"login" | "api">("login");
const editingId = ref<string | null>(null);
const provider = ref<ApiProvider>("deepseek");
const baseUrl = ref("");
const apiKey = ref("");
const models = ref<Required<ApiModels>>({ ...presets.deepseek.models });
const formError = ref("");
function applyPreset(p: ApiProvider) {
  provider.value = p;
  baseUrl.value = presets[p].base_url;
  models.value = { ...presets[p].models };
}
function resetForm() {
  adding.value = false;
  editingId.value = null;
  apiKey.value = "";
  formError.value = "";
}
const login = ref<{ accountId: string; command: string; env: string } | null>(
  null,
);
const remembered = new Map<string, { command: string; env: string }>();

async function run(action: () => Promise<unknown>) {
  busy.value = true;
  try {
    await action();
    emit("changed");
    return true;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    if (adding.value) formError.value = msg;
    else emit("error", msg);
    return false;
  } finally {
    busy.value = false;
  }
}
async function toggle(el: HTMLInputElement) {
  const ok = await run(() =>
    api.post(`${base.value}/enabled`, { enabled: el.checked }),
  );
  if (!ok) el.checked = props.agent.enabled;
}

async function startAdd() {
  menu.value = false;
  adding.value = true;
  editingId.value = null;
  login.value = null;
  label.value = "";
  authMode.value = "login";
  formError.value = "";
  apiKey.value = "";
  applyPreset("deepseek");
  await nextTick();
  document.getElementById(`acct-label-${props.agent.id}`)?.focus();
}
function startEdit(a: AgentAccount) {
  if (!a.api) return;
  adding.value = true;
  editingId.value = a.id;
  login.value = null;
  label.value = a.label;
  authMode.value = "api";
  formError.value = "";
  apiKey.value = "";
  provider.value = a.api.provider;
  baseUrl.value = a.api.base_url;
  models.value = {
    opus: a.api.models.opus ?? "",
    sonnet: a.api.models.sonnet ?? "",
    haiku: a.api.models.haiku ?? "",
  };
  void nextTick(() =>
    document.getElementById(`acct-base-${props.agent.id}`)?.focus(),
  );
}
function apiBody() {
  const m: ApiModels = {};
  for (const k of modelSlots) {
    const v = models.value[k].trim();
    if (v) m[k] = v;
  }
  return {
    provider: provider.value,
    base_url: baseUrl.value.trim(),
    api_key: apiKey.value,
    models: m,
  };
}
async function saveAccount() {
  const name = label.value.trim();
  if (!name) return;
  formError.value = "";
  if (editingId.value) {
    const ok = await run(() =>
      api.put(`${base.value}/accounts/${editingId.value}/api`, apiBody()),
    );
    if (ok) resetForm();
    return;
  }
  if (isClaude.value && authMode.value === "api") {
    const ok = await run(() =>
      api.post<NewAccount>(`${base.value}/accounts`, {
        label: name,
        api: apiBody(),
      }),
    );
    if (ok) resetForm();
    return;
  }
  await createAccount(name);
}
async function createAccount(name: string) {
  await run(async () => {
    const res = await api.post<NewAccount>(`${base.value}/accounts`, {
      label: name,
    });
    const info = { command: res.login_command, env: res.env_hint ?? "" };
    remembered.set(res.account.id, info);
    login.value = { accountId: res.account.id, ...info };
    adding.value = false;
  });
}
function showSignIn(a: AgentAccount) {
  const known = remembered.get(a.id);
  login.value = {
    accountId: a.id,
    command:
      known?.command ??
      a.login_command ??
      (a.system ? genericLogin[props.agent.id] : ""),
    env: known?.env ?? "",
  };
}
async function signedIn() {
  const l = login.value;
  if (!l) return;
  const ok = await run(() =>
    api.post(`${base.value}/accounts/${l.accountId}/refresh`),
  );
  if (ok) login.value = null;
}
const copy = (text: string) => void navigator.clipboard?.writeText(text);
const use = (a: AgentAccount) =>
  run(() => api.post(`${base.value}/accounts/${a.id}/activate`));
function remove(a: AgentAccount) {
  if (!window.confirm(`Remove the account "${a.label}"? Its login is deleted.`))
    return;
  void run(() => api.del(`${base.value}/accounts/${a.id}`));
}
const roleLabels: Record<string, string> = {
  foreman: "Foreman",
  architect: "Architect",
  builder: "Builder",
  inspector: "Inspector",
  chat: "Chat",
};
const chipRoles = computed(() =>
  Object.keys(roleLabels).filter((r) =>
    (props.agent.roles_allowed as string[]).includes(r),
  ),
);
const holds = (a: AgentAccount, r: string) => (a.roles ?? []).includes(r);
const unassigned = computed(() =>
  chipRoles.value.filter((r) => !props.agent.accounts.some((x) => holds(x, r))),
);
const coversUnassigned = (a: AgentAccount) =>
  a.active && (chipRoles.value.length === 0 || unassigned.value.length > 0);
const holdsRole = (a: AgentAccount) => chipRoles.value.some((r) => holds(a, r));
const inUse = (a: AgentAccount) => holdsRole(a) || coversUnassigned(a);
const mappedLine = (a: AgentAccount) =>
  a.api
    ? [
        a.api.host,
        ...modelSlots
          .filter((k) => a.api!.models[k])
          .map((k) => `${k}→${a.api!.models[k]}`),
      ].join(" · ")
    : "";
const heldElsewhere = (a: AgentAccount, r: string) =>
  holds(a, r)
    ? undefined
    : props.agent.accounts.find((x) => x.id !== a.id && holds(x, r));
const chipLabel = (a: AgentAccount, r: string) => {
  const other = heldElsewhere(a, r);
  return other ? `${roleLabels[r]} · on ${other.label}` : roleLabels[r];
};

// the chips build their body from props, so they stay locked until the parent's reload hands over a new agent
const settling = ref(false);
let settleTimer: ReturnType<typeof setTimeout> | undefined;
watch(
  () => props.agent,
  () => {
    settling.value = false;
    clearTimeout(settleTimer);
  },
);
onBeforeUnmount(() => clearTimeout(settleTimer));
async function toggleRole(a: AgentAccount, r: string) {
  const ok = await run(() =>
    api.put(`${base.value}/accounts/${a.id}/roles`, {
      roles: holds(a, r)
        ? (a.roles ?? []).filter((x) => x !== r)
        : [...(a.roles ?? []), r],
    }),
  );
  if (!ok) return;
  settling.value = true;
  clearTimeout(settleTimer);
  settleTimer = setTimeout(() => (settling.value = false), 5000);
}
const refresh = () =>
  run(async () => {
    if (active.value)
      await api.post(`${base.value}/accounts/${active.value.id}/refresh`);
  });

const meters = (a: AgentAccount) =>
  [
    { key: "session", name: "Session", w: a.usage?.session },
    { key: "week", name: "Week", w: a.usage?.week },
  ].filter((m) => m.w);
const planLine = (a: AgentAccount) =>
  [
    a.plan,
    a.expires_at
      ? `login expires ${new Date(a.expires_at).toLocaleDateString()}`
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
const signInTarget = computed(() =>
  active.value?.status === "signed_out"
    ? active.value
    : active.value
      ? undefined
      : props.agent.accounts.find((a) => a.status === "signed_out"),
);
function weekOf(a?: AgentAccount) {
  if (!a?.api) return "";
  const w = props.week?.[`${props.agent.id}/${a.id}`] ?? { tokens: 0, runs: 0 };
  return `Last 7 days: ${compact.format(w.tokens)} tokens · ${w.runs} runs`;
}
const weekLine = computed(() => weekOf(active.value));
const compact = new Intl.NumberFormat("en-US", { notation: "compact" });
const showBars = computed(() => {
  const a = active.value;
  if (!a) return false;
  return !!(a.usage?.session || a.usage?.week || a.usage_error);
});
</script>
<template>
  <div class="agent-acct" :data-agent="agent.id">
    <div class="agent-head">
      <span
        class="provider-logo"
        :data-tone="toneOf(agent.name)"
        aria-hidden="true"
        >{{ agent.name[0] }}</span
      >
      <div class="grow">
        <h3>
          {{ agent.name }} <span class="mono">{{ agent.command }}</span>
        </h3>
        <p v-if="!agent.enabled" class="status">Turned off</p>
        <p
          v-else-if="!active || active.status === 'signed_out'"
          class="status bad"
        >
          Not signed in
        </p>
        <p v-else class="status">
          {{ active.who || active.label }}
          <template v-if="active.status === 'unknown'">
            · {{ active.status_note || "status unknown" }}</template
          >
        </p>
      </div>
      <button
        v-if="agent.enabled && canWrite && signInTarget"
        class="btn primary small"
        :disabled="busy"
        @click="showSignIn(signInTarget)"
      >
        Sign in
      </button>
      <label class="switch"
        ><input
          type="checkbox"
          :aria-label="`Use ${agent.name}`"
          :checked="agent.enabled"
          :disabled="!canWrite || busy"
          @change="toggle($event.target as HTMLInputElement)" /><span></span
      ></label>
      <Popover v-if="canWrite" v-model:open="menu">
        <template #trigger="{ toggle: t, open }">
          <button
            class="btn small icon-only"
            type="button"
            :aria-label="`More for ${agent.name}`"
            aria-haspopup="menu"
            :aria-expanded="open"
            @click="t"
          >
            <MoreHorizontal class="icon" aria-hidden="true" />
          </button>
        </template>
        <button
          type="button"
          role="menuitem"
          :disabled="!credentialsConfigured"
          @click="startAdd"
        >
          Add account
        </button>
        <button type="button" role="menuitem" @click="refresh">
          Refresh status
        </button>
      </Popover>
    </div>

    <template v-if="agent.enabled && active?.status === 'signed_in'">
      <p v-if="!showBars" class="help" data-test="plan-line">
        {{ planLine(active) }}
      </p>
      <div v-else class="usage">
        <p v-if="active.usage_error" class="help" data-test="usage-error">
          Usage unavailable
        </p>
        <div
          v-for="m in meters(active)"
          :key="m.key"
          class="meter"
          :data-meter="m.key"
        >
          <div class="lbl">
            <span>{{ m.name }} · {{ resetsIn(m.w!.resets_at) }}</span
            ><span>{{ Math.round(m.w!.used_pct) }}%</span>
          </div>
          <div
            class="bar"
            role="progressbar"
            :aria-label="`${agent.name} ${m.name.toLowerCase()} usage`"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="Math.round(m.w!.used_pct)"
          >
            <i
              :class="{ warn: m.w!.used_pct > 85 }"
              :style="{ width: Math.min(100, m.w!.used_pct) + '%' }"
            ></i>
          </div>
        </div>
      </div>
      <p v-if="weekLine" class="help" data-test="week-line">{{ weekLine }}</p>
    </template>

    <div v-if="agent.accounts.length > 1" class="accounts">
      <div
        v-for="a in agent.accounts"
        :key="a.id"
        class="acct"
        :class="{ active: a.active, idle: !inUse(a) }"
        :data-state="inUse(a) ? 'in-use' : 'idle'"
      >
        <div class="grow">
          <b>{{ a.label }}</b>
          <span class="help">
            {{ a.who
            }}<template v-if="a.status === 'signed_out'"> · signed out</template
            ><template v-if="a.status_note">
              · {{ a.status_note }}</template
            ></span
          >
          <span v-if="a.api" class="help mono" data-test="mapped-models">{{
            mappedLine(a)
          }}</span>
          <span v-if="a.api" class="help" data-test="account-week-line">{{
            weekOf(a)
          }}</span>
        </div>
        <button
          v-if="canWrite && a.status === 'signed_out' && a !== signInTarget"
          class="btn small"
          :aria-label="`Sign in ${a.label}`"
          :disabled="busy"
          @click="showSignIn(a)"
        >
          Sign in
        </button>
        <span v-if="!inUse(a)" class="pill idle-pill">Idle</span>
        <span v-else-if="coversUnassigned(a)" class="pill green"
          >In use · unassigned roles</span
        >
        <span v-else class="pill green">In use</span>
        <button
          v-if="canWrite && !a.active"
          class="btn small"
          :aria-label="`Use ${a.label}`"
          :disabled="busy"
          @click="use(a)"
        >
          Use
        </button>
        <button
          v-if="canWrite && a.api"
          class="btn small"
          :aria-label="`Edit ${a.label}`"
          :disabled="busy"
          @click="startEdit(a)"
        >
          Edit
        </button>
        <button
          v-if="canWrite && !a.system"
          class="btn small"
          :aria-label="`Remove ${a.label}`"
          :disabled="busy"
          @click="remove(a)"
        >
          Remove
        </button>
        <div
          v-if="chipRoles.length"
          class="role-chips"
          role="group"
          :aria-label="`Roles for ${a.label}`"
        >
          <button
            v-for="r in chipRoles"
            :key="r"
            type="button"
            class="role-chip"
            :aria-pressed="holds(a, r)"
            :disabled="!canWrite || busy || settling"
            :title="chipLabel(a, r)"
            :aria-label="chipLabel(a, r)"
            @click="toggleRole(a, r)"
          >
            {{ roleLabels[r]
            }}<small v-if="heldElsewhere(a, r)" class="chip-holder">
              · {{ heldElsewhere(a, r)!.label }}</small
            >
          </button>
        </div>
      </div>
    </div>

    <form
      v-if="adding"
      class="inline-form"
      data-test="add-account"
      @submit.prevent="saveAccount"
    >
      <div class="form-group">
        <label :for="`acct-label-${agent.id}`">Name for this account</label>
        <input
          :id="`acct-label-${agent.id}`"
          v-model="label"
          maxlength="60"
          placeholder="Personal"
          :readonly="!!editingId"
          required
        />
      </div>
      <div
        v-if="isClaude && !editingId"
        class="auth-switch"
        role="group"
        aria-label="How this account signs in"
      >
        <button
          type="button"
          class="role-chip"
          :aria-pressed="authMode === 'login'"
          @click="authMode = 'login'"
        >
          Subscription login
        </button>
        <button
          type="button"
          class="role-chip"
          :aria-pressed="authMode === 'api'"
          @click="authMode = 'api'"
        >
          API key
        </button>
      </div>
      <template v-if="isClaude && authMode === 'api'">
        <div class="form-group">
          <label :for="`acct-provider-${agent.id}`">Provider</label>
          <select
            :id="`acct-provider-${agent.id}`"
            :value="provider"
            @change="
              applyPreset(
                ($event.target as HTMLSelectElement).value as ApiProvider,
              )
            "
          >
            <option v-for="(p, k) in presets" :key="k" :value="k">
              {{ p.name }}
            </option>
          </select>
        </div>
        <div class="form-group">
          <label :for="`acct-base-${agent.id}`">Base URL</label>
          <input
            :id="`acct-base-${agent.id}`"
            v-model="baseUrl"
            type="url"
            placeholder="https://api.example.com/anthropic"
            autocomplete="off"
            required
          />
        </div>
        <div class="form-group">
          <label :for="`acct-key-${agent.id}`">API key</label>
          <input
            :id="`acct-key-${agent.id}`"
            v-model="apiKey"
            type="password"
            autocomplete="off"
            :placeholder="
              editingId
                ? 'Leave blank to keep the current key'
                : 'Paste the key'
            "
            :required="!editingId"
          />
        </div>
        <div class="model-fields">
          <div v-for="k in modelSlots" :key="k" class="form-group">
            <label :for="`acct-model-${k}-${agent.id}`">{{
              k[0].toUpperCase() + k.slice(1)
            }}</label>
            <input
              :id="`acct-model-${k}-${agent.id}`"
              v-model="models[k]"
              autocomplete="off"
              placeholder="Model name"
            />
          </div>
        </div>
      </template>
      <p v-if="formError" class="help bad" role="alert">{{ formError }}</p>
      <div class="row">
        <button class="btn primary small" type="submit" :disabled="busy">
          {{ editingId ? "Save changes" : "Create account" }}
        </button>
        <button class="btn small" type="button" @click="resetForm">
          Cancel
        </button>
      </div>
    </form>
    <div v-if="login" class="inline-form" data-test="login-steps">
      <template v-if="login.command">
        <p class="help">
          Run this in a terminal and sign in, then come back here.
        </p>
        <div class="cmd">
          <code>{{ login.command }}</code><button
            class="btn small icon-only"
            type="button"
            aria-label="Copy login command"
            @click="copy(login.command)"
          >
            <Copy class="icon" aria-hidden="true" />
          </button>
        </div>
        <p v-if="login.env" class="help">{{ login.env }}</p>
      </template>
      <p v-else class="help">
        This account's login command was only shown when it was created. Remove
        it and add it again to get a new one.
      </p>
      <div class="row">
        <button
          class="btn primary small"
          type="button"
          :disabled="busy"
          @click="signedIn"
        >
          I've signed in
        </button>
        <button class="btn small" type="button" @click="login = null">
          Close
        </button>
      </div>
    </div>
  </div>
</template>
