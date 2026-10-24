<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import type { Connection, Provider } from "../../types/api";

const factory = useFactory(),
  session = useSession();
const busy = ref(""),
  error = ref(""),
  loadError = ref("");

const providers: Record<
  Provider,
  { name: string; base: string; user: string; help: string }
> = {
  github: {
    name: "GitHub",
    base: "https://api.github.com",
    user: "",
    help: "Personal access token with Pull requests and Contents write access. Enterprise: https://<host>/api/v3.",
  },
  gitlab: {
    name: "GitLab",
    base: "https://gitlab.com/api/v4",
    user: "",
    help: "Personal or project access token with the api scope. Self-managed: https://<host>/api/v4.",
  },
  bitbucket: {
    name: "Bitbucket Cloud",
    base: "https://api.bitbucket.org/2.0",
    user: "Bitbucket username",
    help: "Username plus an API token or app password with pull request and repository write access.",
  },
  jira: {
    name: "Jira Cloud",
    base: "",
    user: "Account email",
    help: "Site URL such as https://acme.atlassian.net, your account email and an API token. Read-only.",
  },
};

const form = reactive({
  provider: "github" as Provider,
  name: "",
  scope: "personal" as "shared" | "personal",
  base_url: "",
  username: "",
  token: "",
});
const meta = computed(() => providers[form.provider]);

onMounted(async () => {
  try {
    await factory.refreshConnections();
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
});

function canManage(c: Connection) {
  return c.scope === "shared"
    ? session.isAdmin
    : c.owner_id === session.user?.id;
}

async function run(key: string, action: () => Promise<unknown>) {
  busy.value = key;
  error.value = "";
  try {
    await action();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = "";
  }
}

async function create() {
  await run("create", async () => {
    await factory.createConnection({
      ...form,
      base_url: form.base_url.trim(),
      username: form.username.trim(),
      token: form.token.trim(),
    });
    Object.assign(form, { name: "", base_url: "", username: "", token: "" });
  });
}

function revoke(c: Connection) {
  if (!window.confirm(`Revoke "${c.name}"? Its stored credential is deleted.`))
    return;
  void run(c.id, () => factory.deleteConnection(c.id));
}
</script>
<template>
  <section class="settings-section">
    <h2>Connections, with clear ownership.</h2>
    <p>
      Shared connections are managed by admins and can be bound to projects for
      pull requests. Personal connections are visible and usable only by you.
      Credentials are encrypted at rest and never shown again.
    </p>
    <div class="connection">
      <span class="provider-logo">O</span>
      <div class="grow">
        <h3>OpenCode</h3>
        <p>
          {{
            factory.models.some((m) => m.harness === "opencode")
              ? "Models discovered from OpenCode."
              : "OpenCode is not reported; scripted mock runs are labeled mock."
          }}
        </p>
      </div>
      <span
        class="pill"
        :class="
          factory.models.some((m) => m.harness === 'opencode') ? 'green' : ''
        "
        >{{
          factory.models.some((m) => m.harness === "opencode")
            ? "Discovered"
            : "Not reported"
        }}</span
      >
    </div>
    <p v-if="loadError" class="notice" role="alert">{{ loadError }}</p>
    <div
      v-for="c in factory.connections"
      :key="c.id"
      class="connection"
      :data-connection="c.provider"
    >
      <span class="provider-logo">{{ providers[c.provider].name[0] }}</span>
      <div class="grow">
        <h3>{{ c.name }}</h3>
        <p>
          {{ providers[c.provider].name }} · {{ c.base_url }}
          <template v-if="c.account"> · {{ c.account }}</template>
        </p>
        <p v-if="c.last_error" class="form-error">{{ c.last_error }}</p>
      </div>
      <div class="connection-meta">
        <span class="pill">{{
          c.scope === "shared" ? "Shared" : "Personal"
        }}</span>
        <span
          class="pill"
          :class="
            c.status === 'disabled' ? 'red' : c.verified_at ? 'green' : 'orange'
          "
          >{{
            c.status === "disabled"
              ? "Disabled"
              : c.verified_at
                ? "Verified"
                : "Unverified"
          }}</span
        >
        <template v-if="canManage(c)">
          <button
            class="btn small"
            :disabled="!!busy"
            @click="run(c.id, () => factory.testConnection(c.id))"
          >
            Test</button
          ><button
            class="btn small"
            :disabled="!!busy"
            @click="
              run(c.id, () =>
                factory.updateConnection(c.id, {
                  status: c.status === 'active' ? 'disabled' : 'active',
                }),
              )
            "
          >
            {{ c.status === "active" ? "Disable" : "Enable" }}</button
          ><button class="btn small" :disabled="!!busy" @click="revoke(c)">
            Revoke
          </button>
        </template>
      </div>
    </div>
    <p v-if="!factory.connections.length && !loadError" class="help">
      No provider connections yet.
    </p>
  </section>

  <section v-if="session.canWrite" class="settings-section">
    <h2>Add a connection</h2>
    <p v-if="!factory.credentialsConfigured" class="notice">
      Credential storage is not configured on the server (FACTORY_SECRET_KEY).
      Connections cannot be added.
    </p>
    <form
      v-else
      class="form-grid"
      data-test="connection-form"
      @submit.prevent="create"
    >
      <div class="form-group">
        <label for="conn-provider">Provider</label
        ><select id="conn-provider" v-model="form.provider">
          <option v-for="(p, key) in providers" :key="key" :value="key">
            {{ p.name }}
          </option>
        </select>
      </div>
      <div class="form-group">
        <label for="conn-name">Name</label
        ><input
          id="conn-name"
          v-model="form.name"
          required
          maxlength="100"
          placeholder="Acme GitHub"
        />
      </div>
      <div class="form-group">
        <label for="conn-scope">Scope</label
        ><select id="conn-scope" v-model="form.scope">
          <option value="personal">Personal (only me)</option>
          <option value="shared" :disabled="!session.isAdmin">
            Shared (workspace{{ session.isAdmin ? "" : " · admin only" }})
          </option>
        </select>
      </div>
      <div class="form-group">
        <label for="conn-base">Base URL</label
        ><input
          id="conn-base"
          v-model="form.base_url"
          :required="!meta.base"
          :placeholder="meta.base || 'https://acme.atlassian.net'"
        />
      </div>
      <div v-if="meta.user" class="form-group">
        <label for="conn-user">{{ meta.user }}</label
        ><input
          id="conn-user"
          v-model="form.username"
          required
          autocomplete="off"
        />
      </div>
      <div class="form-group">
        <label for="conn-token">Token</label
        ><input
          id="conn-token"
          v-model="form.token"
          type="password"
          required
          autocomplete="new-password"
        />
        <p class="help">{{ meta.help }}</p>
      </div>
      <div class="form-group" style="align-self: end">
        <button class="btn primary" type="submit" :disabled="!!busy">
          {{ busy === "create" ? "Saving…" : "Add connection" }}
        </button>
      </div>
    </form>
  </section>
  <p v-if="error" class="notice" role="alert">{{ error }}</p>
  <div class="notice">
    New connections stay unverified until Test succeeds. Pull requests are
    opened only after a human approves a work order in a bound project; Jira is
    read-only.
  </div>
</template>
