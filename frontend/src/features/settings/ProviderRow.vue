<script setup lang="ts">
import { toneOf } from "./tone";
import { computed, reactive, ref } from "vue";
import { MoreHorizontal } from "lucide-vue-next";
import Popover from "../../components/ui/Popover.vue";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import type { Connection, ConnectionProvider, Provider } from "../../types/api";

const props = defineProps<{
  provider: ConnectionProvider;
  credentialsConfigured: boolean;
}>();
const emit = defineEmits<{ error: [string] }>();
const factory = useFactory(),
  session = useSession();

const kinds: Record<
  Provider,
  { name: string; desc: string; base: string; user: string; help: string }
> = {
  github: {
    name: "GitHub",
    desc: "Repos and pull requests",
    base: "https://api.github.com",
    user: "",
    help: "Personal access token with Pull requests and Contents write access.",
  },
  gitlab: {
    name: "GitLab",
    desc: "Repos and merge requests",
    base: "https://gitlab.com/api/v4",
    user: "",
    help: "Personal or project access token with the api scope.",
  },
  bitbucket: {
    name: "Bitbucket",
    desc: "Repos and pull requests",
    base: "https://api.bitbucket.org/2.0",
    user: "Bitbucket username",
    help: "Username plus an API token or app password with pull request and repository write access.",
  },
  jira: {
    name: "Jira",
    desc: "Tickets for briefs, read only",
    base: "",
    user: "Account email",
    help: "Site URL such as acme.atlassian.net, your account email and an API token.",
  },
};
const meta = computed(() => kinds[props.provider.id]);
const id = computed(() => props.provider.id);

const open = ref(false),
  more = ref(false),
  busy = ref("");
const form = reactive({
  token: "",
  base: "",
  username: "",
});
const edit = reactive({ id: "", name: "", token: "" });
const menus = reactive<Record<string, boolean>>({});

const canManage = (c: Connection) => c.owner_id === session.user?.id;

async function run(key: string, action: () => Promise<unknown>) {
  busy.value = key;
  try {
    await action();
    return true;
  } catch (e) {
    emit("error", e instanceof Error ? e.message : String(e));
    return false;
  } finally {
    busy.value = "";
  }
}
function uniqueName() {
  const names = new Set(props.provider.accounts.map((a) => a.name));
  let n = meta.value.name,
    i = 1;
  while (names.has(n)) n = `${meta.value.name} ${++i}`;
  return n;
}
function baseUrl() {
  const v = form.base.trim();
  if (id.value === "jira") return /^https?:\/\//.test(v) ? v : `https://${v}`;
  return v;
}
async function connect() {
  const ok = await run("connect", () =>
    factory.createConnection({
      provider: id.value,
      name: uniqueName(),
      base_url: baseUrl(),
      username: form.username.trim(),
      token: form.token.trim(),
    }),
  );
  if (ok) {
    Object.assign(form, { token: "", base: "", username: "" });
    open.value = more.value = false;
  }
}
function disconnect(c: Connection) {
  if (
    !window.confirm(`Disconnect "${c.name}"? Its stored credential is deleted.`)
  )
    return;
  void run(c.id, () => factory.deleteConnection(c.id));
}
async function saveEdit(c: Connection) {
  const patch: { name?: string; token?: string } = {};
  if (edit.name.trim() && edit.name.trim() !== c.name)
    patch.name = edit.name.trim();
  if (edit.token.trim()) patch.token = edit.token.trim();
  if (Object.keys(patch).length) {
    if (!(await run(c.id, () => factory.updateConnection(c.id, patch)))) return;
  }
  edit.id = "";
}
const state = (c: Connection) =>
  c.status === "disabled"
    ? ["red", "Turned off"]
    : c.verified_at
      ? ["green", "Working"]
      : ["orange", "Not verified"];
</script>
<template>
  <div class="provider" :data-connection="id">
    <div class="agent-head">
      <span
        class="provider-logo"
        :data-tone="toneOf(meta.name)"
        aria-hidden="true"
        >{{ meta.name[0] }}</span
      >
      <div class="grow">
        <h3>{{ meta.name }}</h3>
        <p>{{ meta.desc }}</p>
      </div>
      <button
        v-if="session.canWrite"
        class="btn small"
        type="button"
        :disabled="!credentialsConfigured"
        :aria-label="
          provider.accounts.length
            ? `Add another ${meta.name} account`
            : `Connect ${meta.name}`
        "
        @click="open = !open"
      >
        {{ provider.accounts.length ? "Add account" : "Connect" }}
      </button>
    </div>

    <div
      v-for="c in provider.accounts"
      :key="c.id"
      class="acct"
      :data-account="c.id"
    >
      <div class="grow">
        <b>{{ c.account || c.name }}</b>
        <span class="help">
          <template v-if="c.used_by?.length"
            >Used by {{ c.used_by.map((u) => u.name).join(", ") }}</template
          ></span
        >
        <span v-if="c.last_error" class="form-error">{{ c.last_error }}</span>
      </div>
      <span class="pill" :class="state(c)[0]">{{ state(c)[1] }}</span>
      <Popover v-if="canManage(c)" v-model:open="menus[c.id]">
        <template #trigger="{ toggle, open: isOpen }">
          <button
            class="btn small icon-only"
            type="button"
            :aria-label="`More for ${c.name}`"
            aria-haspopup="menu"
            :aria-expanded="isOpen"
            @click="toggle"
          >
            <MoreHorizontal class="icon" aria-hidden="true" />
          </button>
        </template>
        <button
          type="button"
          role="menuitem"
          :disabled="!!busy"
          @click="run(c.id, () => factory.testConnection(c.id))"
        >
          Test connection
        </button>
        <button
          type="button"
          role="menuitem"
          @click="Object.assign(edit, { id: c.id, name: c.name, token: '' })"
        >
          Change name or token
        </button>
        <button
          type="button"
          role="menuitem"
          :disabled="!!busy"
          @click="
            run(c.id, () =>
              factory.updateConnection(c.id, {
                status: c.status === 'active' ? 'disabled' : 'active',
              }),
            )
          "
        >
          {{ c.status === "active" ? "Turn off" : "Turn on" }}
        </button>
        <button
          type="button"
          role="menuitem"
          class="danger"
          @click="disconnect(c)"
        >
          Disconnect
        </button>
      </Popover>
      <form
        v-if="edit.id === c.id"
        class="inline-form"
        @submit.prevent="saveEdit(c)"
      >
        <div class="form-group">
          <label :for="`conn-rename-${c.id}`">Name</label>
          <input
            :id="`conn-rename-${c.id}`"
            v-model="edit.name"
            maxlength="100"
          />
        </div>
        <div class="form-group">
          <label :for="`conn-rotate-${c.id}`">New token</label>
          <input
            :id="`conn-rotate-${c.id}`"
            v-model="edit.token"
            type="password"
            autocomplete="new-password"
            placeholder="Leave empty to keep the current one"
          />
        </div>
        <div class="row">
          <button class="btn primary small" type="submit" :disabled="!!busy">
            Save
          </button>
          <button class="btn small" type="button" @click="edit.id = ''">
            Cancel
          </button>
        </div>
      </form>
    </div>

    <form
      v-if="open"
      class="inline-form"
      :data-test="`connect-${id}`"
      @submit.prevent="connect"
    >
      <template v-if="id === 'jira'">
        <div class="form-group">
          <label :for="`f-site-${id}`">Site</label>
          <input
            :id="`f-site-${id}`"
            v-model="form.base"
            required
            placeholder="acme.atlassian.net"
          />
        </div>
      </template>
      <div v-if="meta.user" class="form-group">
        <label :for="`f-user-${id}`">{{ meta.user }}</label>
        <input
          :id="`f-user-${id}`"
          v-model="form.username"
          required
          autocomplete="off"
        />
      </div>
      <div class="form-group">
        <label :for="`f-token-${id}`">Access token</label>
        <input
          :id="`f-token-${id}`"
          v-model="form.token"
          type="password"
          required
          autocomplete="new-password"
        />
        <p class="help">{{ meta.help }}</p>
      </div>
      <template v-if="id !== 'jira'">
        <button
          v-if="!more"
          class="btn small link"
          type="button"
          @click="more = true"
        >
          Self-hosted {{ meta.name }}?
        </button>
        <div v-else class="form-group">
          <label :for="`f-base-${id}`">Server URL</label>
          <input
            :id="`f-base-${id}`"
            v-model="form.base"
            :placeholder="meta.base"
          />
        </div>
      </template>
      <div class="row">
        <button class="btn primary small" type="submit" :disabled="!!busy">
          {{ busy === "connect" ? "Saving…" : "Connect" }}
        </button>
        <button class="btn small" type="button" @click="open = false">
          Cancel
        </button>
      </div>
      <p class="help">Saved encrypted and never shown again.</p>
    </form>
  </div>
</template>
