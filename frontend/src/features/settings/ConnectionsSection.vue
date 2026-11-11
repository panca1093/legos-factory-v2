<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Laptop } from "lucide-vue-next";
import { publishLabel } from "../../composables/presentation";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import {
  api,
  type Connection,
  type McpServer,
  type Project,
  type Provider,
} from "../../types/api";
import McpServerRow from "./McpServerRow.vue";
import ProjectBindingRow from "./ProjectBindingRow.vue";

const factory = useFactory(),
  session = useSession();
const busy = ref(""),
  error = ref(""),
  loadError = ref("");
const servers = ref<McpServer[]>([]);

type Kind = Provider | "mcp";
const kinds: Record<
  Kind,
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
  mcp: { name: "MCP server", base: "", user: "", help: "" },
};

const form = reactive({
  kind: "github" as Kind,
  name: "",
  scope: "personal" as "shared" | "personal",
  base_url: "",
  username: "",
  token: "",
});
const mcp = reactive({
  transport: "stdio" as "stdio" | "http",
  command: "",
  args: "",
  url: "",
  secrets: [{ key: "", value: "" }],
});
const isMcp = computed(() => form.kind === "mcp");
const meta = computed(() => kinds[form.kind]);
const secretHelp = computed(() =>
  mcp.transport === "http"
    ? "Sent as request headers, for example Authorization = Bearer <token>."
    : "Passed to the server process as environment variables.",
);

const edit = reactive({ id: "", name: "", token: "" });

const links = reactive<
  Record<string, { git_connection_id: string; remote_repo: string }>
>({});
watch(
  () => factory.projects,
  (projects) => {
    for (const p of projects)
      links[p.id] ??= {
        git_connection_id: p.git_connection_id ?? "",
        remote_repo: p.remote_repo ?? "",
      };
  },
  { immediate: true },
);

async function loadServers() {
  if (!session.isAdmin) return;
  try {
    const res = await api.get<{ servers: McpServer[] }>("/api/v1/mcp-servers");
    servers.value = res.servers ?? [];
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
}
onMounted(async () => {
  factory.refreshProjects().catch(() => {});
  void loadServers();
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
    return true;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    return false;
  } finally {
    busy.value = "";
  }
}

async function create() {
  if (isMcp.value) {
    const secret = Object.fromEntries(
      mcp.secrets
        .filter((s) => s.key.trim())
        .map((s) => [s.key.trim(), s.value]),
    );
    const ok = await run("create", async () => {
      await api.post("/api/v1/mcp-servers", {
        name: form.name.trim(),
        transport: mcp.transport,
        command: mcp.transport === "stdio" ? mcp.command.trim() : "",
        args:
          mcp.transport === "stdio"
            ? mcp.args
                .split("\n")
                .map((a) => a.trim())
                .filter(Boolean)
            : [],
        url: mcp.transport === "http" ? mcp.url.trim() : "",
        secret,
      });
      await loadServers();
    });
    if (ok) {
      Object.assign(form, { name: "" });
      Object.assign(mcp, {
        command: "",
        args: "",
        url: "",
        secrets: [{ key: "", value: "" }],
      });
    }
    return;
  }
  await run("create", async () => {
    await factory.createConnection({
      provider: form.kind,
      name: form.name,
      scope: form.scope,
      base_url: form.base_url.trim(),
      username: form.username.trim(),
      token: form.token.trim(),
    });
    Object.assign(form, { name: "", base_url: "", username: "", token: "" });
  });
}

function remove(c: Connection) {
  if (!window.confirm(`Remove "${c.name}"? Its stored credential is deleted.`))
    return;
  void run(c.id, () => factory.deleteConnection(c.id));
}
function startEdit(c: Connection) {
  Object.assign(edit, { id: c.id, name: c.name, token: "" });
}
async function saveEdit(c: Connection) {
  const patch: { name?: string; token?: string } = {};
  if (edit.name.trim() && edit.name.trim() !== c.name)
    patch.name = edit.name.trim();
  if (edit.token.trim()) patch.token = edit.token.trim();
  if (Object.keys(patch).length) {
    const ok = await run(c.id, () => factory.updateConnection(c.id, patch));
    if (!ok) return;
  }
  edit.id = "";
}

function commitBinding(p: Project) {
  const l = links[p.id];
  void run(`link-${p.id}`, () =>
    factory.bindProject(p.id, {
      git_connection_id: l.git_connection_id,
      remote_repo: l.git_connection_id ? l.remote_repo.trim() : "",
    }),
  );
}
function setContract(p: Project, on: boolean) {
  void run(`contract-${p.id}`, () =>
    factory.setContractTests(p.id, on ? "on" : "off"),
  );
}
</script>
<template>
  <section id="connections" class="settings-section">
    <h2>Connections</h2>
    <p>
      Git providers, Jira and MCP servers. Shared connections are managed by
      admins; personal ones are visible and usable only by you. Credentials are
      encrypted at rest and never shown again.
    </p>
    <p v-if="loadError" class="notice" role="alert">{{ loadError }}</p>

    <div
      v-for="c in factory.connections"
      :key="c.id"
      class="connection"
      :data-connection="c.provider"
      style="flex-wrap: wrap"
    >
      <span class="provider-logo">{{ kinds[c.provider].name[0] }}</span>
      <div class="grow">
        <h3>{{ c.name }}</h3>
        <p>
          {{ kinds[c.provider].name }} · {{ c.base_url }}
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
          ><button class="btn small" :disabled="!!busy" @click="startEdit(c)">
            Rename or rotate token</button
          ><button class="btn small" :disabled="!!busy" @click="remove(c)">
            Remove
          </button>
        </template>
      </div>
      <form
        v-if="edit.id === c.id"
        class="form-grid"
        style="flex-basis: 100%"
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

    <McpServerRow
      v-for="s in servers"
      :key="s.id"
      :server="s"
      :can-manage="session.isAdmin"
      @changed="loadServers"
    />
    <p
      v-if="!factory.connections.length && !servers.length && !loadError"
      class="help"
    >
      No connections yet.
    </p>
    <p v-if="error" class="notice" role="alert">{{ error }}</p>
    <p class="help">
      New connections stay unverified until Test succeeds. Jira is read-only.
      MCP servers are managed by admins and give agents extra tools only where
      you enable them.
    </p>

    <h3 style="margin: 18px 0 8px">Add a connection</h3>
    <p v-if="!factory.credentialsConfigured" class="notice">
      Credential storage is not set up on the server, so connections that need a
      token cannot be added. See Runtime below.
    </p>
    <form
      v-else-if="session.canWrite"
      class="form-grid"
      data-test="connection-form"
      @submit.prevent="create"
    >
      <div class="form-group">
        <label for="conn-provider">Type</label
        ><select id="conn-provider" v-model="form.kind">
          <option
            v-for="(k, key) in kinds"
            :key="key"
            :value="key"
            :disabled="key === 'mcp' && !session.isAdmin"
          >
            {{ k.name
            }}{{ key === "mcp" && !session.isAdmin ? " · admin only" : "" }}
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
          :placeholder="isMcp ? 'docs_search' : 'Acme GitHub'"
        />
        <p v-if="isMcp" class="help">
          Lowercase letters and digits, words joined by single underscores.
        </p>
      </div>

      <template v-if="!isMcp">
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
      </template>

      <template v-else>
        <div class="form-group">
          <label for="mcp-transport">Transport</label
          ><select id="mcp-transport" v-model="mcp.transport">
            <option value="stdio">Local command (stdio)</option>
            <option value="http">Remote URL (http)</option>
          </select>
        </div>
        <template v-if="mcp.transport === 'stdio'">
          <div class="form-group">
            <label for="mcp-command">Command</label
            ><input
              id="mcp-command"
              v-model="mcp.command"
              required
              placeholder="/usr/local/bin/my-mcp-server"
            />
            <p class="help">Absolute path to the executable.</p>
          </div>
          <div class="form-group">
            <label for="mcp-args">Arguments</label
            ><textarea
              id="mcp-args"
              v-model="mcp.args"
              rows="2"
              placeholder="One per line"
            ></textarea>
          </div>
        </template>
        <div v-else class="form-group">
          <label for="mcp-url">URL</label
          ><input
            id="mcp-url"
            v-model="mcp.url"
            required
            placeholder="https://mcp.example.com/mcp"
          />
        </div>
        <div class="form-group">
          <span class="form-label">Secrets</span>
          <div v-for="(s, i) in mcp.secrets" :key="i" class="row">
            <input
              v-model="s.key"
              aria-label="Secret name"
              :placeholder="
                mcp.transport === 'http' ? 'Authorization' : 'API_TOKEN'
              "
            />
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
            @click="mcp.secrets.push({ key: '', value: '' })"
          >
            Add secret
          </button>
          <p class="help">
            {{ secretHelp }} Stored encrypted, never shown again.
          </p>
        </div>
      </template>

      <div class="form-group" style="align-self: end">
        <button class="btn primary" type="submit" :disabled="!!busy">
          {{ busy === "create" ? "Saving…" : "Add connection" }}
        </button>
      </div>
    </form>

    <h3 style="margin: 24px 0 8px">Projects</h3>
    <p class="help">
      Where each project publishes, and whether acceptance criteria get locked
      contract tests.
    </p>
    <div
      v-for="p in factory.projects"
      :key="p.id"
      class="connection"
      :data-project-link="p.id"
    >
      <div class="grow">
        <h3>{{ p.name }}</h3>
        <p>
          <Laptop class="icon proj-local" aria-hidden="true" />
          {{ p.repo_url }} · {{ publishLabel(p) }}
        </p>
        <div v-if="links[p.id]" style="margin-top: 8px">
          <ProjectBindingRow
            :id-prefix="`proj-${p.id}`"
            :label="p.name"
            :repo-url="p.repo_url"
            v-model:connection-id="links[p.id].git_connection_id"
            v-model:remote-repo="links[p.id].remote_repo"
            :contract-tests="p.contract_tests === 'on'"
            :disabled="!!busy || !session.canWrite"
            @update:contract-tests="setContract(p, $event)"
            @commit="commitBinding(p)"
          />
        </div>
      </div>
    </div>
    <p v-if="!factory.projects.length" class="help">No projects yet.</p>
  </section>
</template>
