<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { api, MCP_ROLES, type McpServer } from "../../types/api";

const props = defineProps<{ server: McpServer; canManage: boolean }>();
const emit = defineEmits<{ changed: [] }>();

const open = ref(false),
  editing = ref(false),
  busy = ref(false),
  error = ref("");
const edit = reactive({
  name: "",
  secrets: [] as { key: string; value: string }[],
});
const base = computed(() => `/api/v1/mcp-servers/${props.server.id}`);
const builderOnly = computed(
  () => props.server.roles.length === 1 && props.server.roles[0] === "builder",
);
const discovered = computed(() => !!props.server.discovered_at);
const enabledCount = computed(
  () => props.server.tools.filter((t) => t.enabled).length,
);
const target = computed(() =>
  props.server.transport === "http"
    ? props.server.url
    : [props.server.command, ...(props.server.args ?? [])].join(" "),
);

async function run(action: () => Promise<unknown>) {
  busy.value = true;
  error.value = "";
  try {
    await action();
    emit("changed");
    return true;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    return false;
  } finally {
    busy.value = false;
  }
}
async function signIn() {
  const win = window.open("", "_blank");
  const ok = await run(async () => {
    const res = await api.post<{ authorize_url: string }>(
      `${base.value}/oauth/start`,
    );
    if (win) win.location.href = res.authorize_url;
    else window.location.assign(res.authorize_url);
  });
  if (!ok) win?.close();
}
const isOAuth = computed(() => props.server.auth === "oauth");
const expiry = computed(() =>
  props.server.expires_at
    ? new Date(props.server.expires_at).toLocaleString()
    : "",
);
const discover = () => run(() => api.post(`${base.value}/discover`));
const toggleStatus = () =>
  run(() =>
    api.patch(base.value, {
      status: props.server.status === "disabled" ? "active" : "disabled",
    }),
  );
async function toggleRole(role: string, el: HTMLInputElement) {
  const ok = await run(() =>
    api.put(`${base.value}/roles`, {
      roles: MCP_ROLES.filter((r) =>
        r === role ? el.checked : props.server.roles.includes(r),
      ),
    }),
  );
  if (!ok) el.checked = !el.checked;
}
async function toggleTool(name: string, el: HTMLInputElement) {
  const ok = await run(() =>
    api.put(`${base.value}/tools`, [{ name, enabled: el.checked }]),
  );
  if (!ok) el.checked = !el.checked;
}
function remove() {
  if (
    !window.confirm(`Remove "${props.server.name}"? Its secrets are deleted.`)
  )
    return;
  void run(() => api.del(base.value));
}
function startEdit() {
  edit.name = props.server.name;
  edit.secrets = [];
  editing.value = true;
}
async function saveEdit() {
  const patch: Record<string, unknown> = {};
  if (edit.name.trim() !== props.server.name) patch.name = edit.name.trim();
  const filled = edit.secrets.filter((s) => s.key.trim() || s.value);
  if (filled.length)
    patch.secret = Object.fromEntries(
      filled.map((s) => [s.key.trim(), s.value]),
    );
  if (!Object.keys(patch).length) {
    editing.value = false;
    return;
  }
  await run(() => api.patch(base.value, patch));
  if (!error.value) editing.value = false;
}
const toolKind = (t: McpServer["tools"][number]) =>
  t.unusable ? "Unusable" : t.read_only ? "Read-only" : "Writing";
const canEnable = (t: McpServer["tools"][number]) =>
  !t.unusable && (t.read_only || builderOnly.value);
function whyLocked(t: McpServer["tools"][number]) {
  if (t.unusable)
    return t.unusable_reason || "Claude Code cannot address this tool name";
  if (!t.read_only && !builderOnly.value)
    return "Writing tools can be enabled only when this server is offered to Builder alone";
  return "";
}
</script>
<template>
  <div class="connection" :data-mcp="server.name">
    <span class="provider-logo">M</span>
    <div class="grow">
      <h3>{{ server.name }}</h3>
      <p>
        MCP · {{ server.transport }} · {{ target }}
        <template v-if="server.secret_keys?.length">
          · secrets: {{ server.secret_keys.join(", ") }}</template
        >
      </p>
      <p v-if="isOAuth" class="help" data-test="mcp-oauth-state">
        OAuth ·
        <template v-if="server.status === 'needs_sign_in' || !server.signed_in"
          >sign-in needed</template
        ><template v-else
          >signed in<template v-if="expiry">
            · access token until {{ expiry }}</template
          ></template
        >
        <template v-if="server.oauth_client">
          · {{ server.oauth_client }} client</template
        >
      </p>
      <p v-if="server.last_error" class="form-error" data-test="mcp-last-error">
        {{ server.last_error }}
      </p>
    </div>
    <div class="connection-meta">
      <span class="pill">Shared</span>
      <span
        class="pill"
        :class="
          server.status === 'disabled'
            ? 'red'
            : discovered && server.status === 'active'
              ? 'green'
              : 'orange'
        "
        >{{
          server.status === "disabled"
            ? "Disabled"
            : server.status === "needs_sign_in"
              ? "Sign-in needed"
              : discovered
                ? `${enabledCount} of ${server.tools.length} tools on`
                : "Not discovered"
        }}</span
      >
      <span v-if="server.approx_tokens" class="pill" data-test="mcp-tokens"
        >~{{ server.approx_tokens }} tokens</span
      >
      <template v-if="canManage">
        <button
          v-if="isOAuth"
          class="btn small primary"
          data-test="mcp-sign-in"
          :disabled="busy"
          @click="signIn"
        >
          {{ server.signed_in ? "Sign in again" : "Sign in" }}
        </button>
        <button class="btn small" :disabled="busy" @click="discover">
          {{ discovered ? "Discover again" : "Discover" }}
        </button>
        <button class="btn small" :disabled="busy" @click="open = !open">
          {{ open ? "Hide tools" : "Tools and roles" }}
        </button>
        <button class="btn small" :disabled="busy" @click="toggleStatus">
          {{ server.status === "disabled" ? "Enable" : "Disable" }}
        </button>
        <button class="btn small" :disabled="busy" @click="startEdit">
          Rename or rotate
        </button>
        <button class="btn small" :disabled="busy" @click="remove">
          Remove
        </button>
      </template>
      <button v-else class="btn small" @click="open = !open">
        {{ open ? "Hide tools" : "Tools and roles" }}
      </button>
    </div>

    <form
      v-if="editing"
      class="form-grid"
      style="flex-basis: 100%"
      @submit.prevent="saveEdit"
    >
      <div class="form-group">
        <label :for="`mcp-rename-${server.id}`">Name</label>
        <input :id="`mcp-rename-${server.id}`" v-model="edit.name" />
      </div>
      <div v-if="!isOAuth" class="form-group">
        <span class="form-label">Replace secrets</span>
        <p class="help">
          Anything entered here replaces all stored secrets. Leave empty to keep
          them.
        </p>
        <div v-for="(s, i) in edit.secrets" :key="i" class="row">
          <input v-model="s.key" aria-label="Secret name" />
          <input
            v-model="s.value"
            aria-label="Secret value"
            type="password"
            autocomplete="new-password"
          />
        </div>
        <button
          class="btn small"
          type="button"
          @click="edit.secrets.push({ key: '', value: '' })"
        >
          Add secret
        </button>
      </div>
      <div class="row">
        <button class="btn primary small" type="submit" :disabled="busy">
          Save
        </button>
        <button class="btn small" type="button" @click="editing = false">
          Cancel
        </button>
      </div>
    </form>

    <div v-if="open" style="flex-basis: 100%">
      <fieldset :disabled="!canManage || busy">
        <legend class="form-label">Offered to</legend>
        <label v-for="r in MCP_ROLES" :key="r" style="margin-right: 14px">
          <input
            type="checkbox"
            :data-test="`mcp-role-${r}`"
            :checked="server.roles.includes(r)"
            @change="toggleRole(r, $event.target as HTMLInputElement)"
          />
          {{ r }}
        </label>
      </fieldset>
      <p v-if="!discovered" class="help">
        Discover the server's tools to choose which ones agents may use.
      </p>
      <div
        v-for="t in server.tools"
        :key="t.name"
        class="switch-row"
        :data-tool="t.name"
      >
        <div>
          <b>{{ t.name }}</b>
          <span
            class="pill"
            :class="t.unusable ? 'red' : t.read_only ? 'green' : 'orange'"
            style="margin-left: 6px"
            >{{ toolKind(t) }}</span
          >
          <span class="help"> · ~{{ t.size }} tokens</span>
          <p class="help">{{ t.description }}</p>
          <p v-if="whyLocked(t)" class="help">{{ whyLocked(t) }}</p>
        </div>
        <label class="switch"
          ><input
            type="checkbox"
            :aria-label="`Enable ${t.name}`"
            :data-test="`mcp-tool-${t.name}`"
            :checked="t.enabled"
            :disabled="!canManage || busy || (!t.enabled && !canEnable(t))"
            @change="
              toggleTool(t.name, $event.target as HTMLInputElement)
            " /><span></span
        ></label>
      </div>
    </div>
    <p v-if="error" class="form-error" role="alert" style="flex-basis: 100%">
      {{ error }}
    </p>
  </div>
</template>

<style scoped>
.connection {
  flex-wrap: wrap;
}
.grow {
  flex-basis: 220px;
  overflow-wrap: anywhere;
}
.connection-meta {
  max-width: 100%;
}
</style>
