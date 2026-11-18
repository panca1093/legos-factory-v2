<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import { Copy, MoreHorizontal } from "lucide-vue-next";
import Popover from "../../components/ui/Popover.vue";
import {
  api,
  type AgentAccount,
  type AgentInfo,
  type NewAccount,
} from "../../types/api";
import { resetsIn } from "../../composables/presentation";

const props = defineProps<{
  agent: AgentInfo;
  canWrite: boolean;
  credentialsConfigured: boolean;
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
    emit("error", e instanceof Error ? e.message : String(e));
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
  login.value = null;
  label.value = "";
  await nextTick();
  document.getElementById(`acct-label-${props.agent.id}`)?.focus();
}
async function createAccount() {
  const name = label.value.trim();
  if (!name) return;
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
const toggleRole = (a: AgentAccount, r: string) =>
  run(() =>
    api.put(`${base.value}/accounts/${a.id}/roles`, {
      roles: holds(a, r)
        ? (a.roles ?? []).filter((x) => x !== r)
        : [...(a.roles ?? []), r],
    }),
  );
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
const showBars = computed(() => {
  const a = active.value;
  if (!a) return false;
  return !!(a.usage?.session || a.usage?.week || a.usage_error);
});
</script>
<template>
  <div class="agent-acct" :data-agent="agent.id">
    <div class="agent-head">
      <span class="provider-logo" aria-hidden="true">{{ agent.name[0] }}</span>
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
            · status unknown</template
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
    </template>

    <div v-if="agent.accounts.length > 1" class="accounts">
      <div
        v-for="a in agent.accounts"
        :key="a.id"
        class="acct"
        :class="{ active: a.active }"
      >
        <div class="grow">
          <b>{{ a.label }}</b>
          <span class="help">
            {{ a.who
            }}<template v-if="a.status === 'signed_out'">
              · signed out</template
            ></span
          >
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
        <span v-if="a.active" class="pill green"
          >In use · unassigned roles</span
        >
        <button
          v-else-if="canWrite"
          class="btn small"
          :aria-label="`Use ${a.label}`"
          :disabled="busy"
          @click="use(a)"
        >
          Use
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
            :disabled="!canWrite || busy"
            @click="toggleRole(a, r)"
          >
            {{ roleLabels[r] }}
          </button>
        </div>
      </div>
    </div>

    <form
      v-if="adding"
      class="inline-form"
      data-test="add-account"
      @submit.prevent="createAccount"
    >
      <div class="form-group">
        <label :for="`acct-label-${agent.id}`">Name for this account</label>
        <input
          :id="`acct-label-${agent.id}`"
          v-model="label"
          maxlength="60"
          placeholder="Personal"
          required
        />
      </div>
      <div class="row">
        <button class="btn primary small" type="submit" :disabled="busy">
          Create account
        </button>
        <button class="btn small" type="button" @click="adding = false">
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
