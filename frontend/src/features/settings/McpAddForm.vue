<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { api } from "../../types/api";

const emit = defineEmits<{ added: []; cancel: []; error: [string] }>();
const busy = ref(false);
const name = ref("");
const mcp = reactive({
  transport: "stdio" as "stdio" | "http",
  command: "",
  args: "",
  url: "",
  auth: "token" as "none" | "token" | "oauth",
  clientId: "",
  clientSecret: "",
  scopes: "",
  secrets: [{ key: "", value: "" }],
});
const secretHelp = computed(() =>
  mcp.transport === "http"
    ? "Sent as request headers, for example Authorization = Bearer <token>."
    : "Passed to the server process as environment variables.",
);

async function create() {
  const oauth = mcp.transport === "http" && mcp.auth === "oauth";
  const secret =
    oauth || (mcp.transport === "http" && mcp.auth === "none")
      ? {}
      : Object.fromEntries(
          mcp.secrets
            .filter((s) => s.key.trim())
            .map((s) => [s.key.trim(), s.value]),
        );
  busy.value = true;
  try {
    await api.post("/api/v1/mcp-servers", {
      name: name.value.trim(),
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
      ...(oauth
        ? {
            auth: "oauth",
            client_id: mcp.clientId.trim(),
            client_secret: mcp.clientSecret.trim(),
            scopes: mcp.scopes.trim(),
          }
        : {}),
    });
    emit("added");
  } catch (e) {
    emit("error", e instanceof Error ? e.message : String(e));
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <form
    class="inline-form form-grid"
    data-test="connection-form"
    @submit.prevent="create"
  >
    <div class="form-group">
      <label for="conn-name">Name</label
      ><input
        id="conn-name"
        v-model="name"
        required
        maxlength="100"
        placeholder="docs_search"
      />
      <p class="help">
        Lowercase letters and digits, words joined by single underscores.
      </p>
    </div>
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
    <div v-if="mcp.transport === 'http'" class="form-group">
      <label for="mcp-auth">Authentication</label
      ><select id="mcp-auth" v-model="mcp.auth">
        <option value="none">None</option>
        <option value="token">Token or header</option>
        <option value="oauth">OAuth sign-in</option>
      </select>
      <p v-if="mcp.auth === 'oauth'" class="help">
        After adding the server, use Sign in to authorize it with its provider.
        The access token is stored encrypted and refreshed automatically.
      </p>
    </div>
    <details
      v-if="mcp.transport === 'http' && mcp.auth === 'oauth'"
      class="form-group"
    >
      <summary>Advanced</summary>
      <div class="form-group">
        <label for="mcp-client-id">Client ID</label
        ><input id="mcp-client-id" v-model="mcp.clientId" autocomplete="off" />
        <p class="help">
          Leave empty to register automatically. Needed when the provider does
          not allow that.
        </p>
      </div>
      <div class="form-group">
        <label for="mcp-client-secret">Client secret</label
        ><input
          id="mcp-client-secret"
          v-model="mcp.clientSecret"
          type="password"
          autocomplete="new-password"
        />
      </div>
      <div class="form-group">
        <label for="mcp-scopes">Scopes</label
        ><input
          id="mcp-scopes"
          v-model="mcp.scopes"
          placeholder="Space-separated; empty uses the provider default"
        />
      </div>
    </details>
    <div
      v-if="mcp.transport === 'stdio' || mcp.auth === 'token'"
      class="form-group"
    >
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
      <p class="help">{{ secretHelp }} Stored encrypted, never shown again.</p>
    </div>
    <div class="row" style="grid-column: 1 / -1">
      <button class="btn primary small" type="submit" :disabled="busy">
        {{ busy ? "Saving…" : "Add MCP server" }}
      </button>
      <button class="btn small" type="button" @click="emit('cancel')">
        Cancel
      </button>
    </div>
  </form>
</template>
