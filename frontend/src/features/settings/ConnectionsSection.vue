<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import { api, type McpServer } from "../../types/api";
import McpAddForm from "./McpAddForm.vue";
import McpServerRow from "./McpServerRow.vue";
import ProviderRow from "./ProviderRow.vue";

defineProps<{ credentialsConfigured: boolean }>();
const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter();
const oauthFlag = ref("");
const error = ref(""),
  loadError = ref("");
const servers = ref<McpServer[]>([]);
const addingMcp = ref(false);

const groups = computed(() => [
  {
    id: "code",
    name: "Code hosting",
    note: "Repos, branches and pull requests",
    providers: factory.providers.filter((p) => p.group === "code"),
  },
  {
    id: "work",
    name: "Work tools",
    note: "Read by agents for context",
    providers: factory.providers.filter((p) => p.group === "work"),
  },
]);

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
  const flag = route.query.mcp_oauth;
  if (flag === "ok" || flag === "error") {
    oauthFlag.value = flag;
    void router.replace({ query: {}, hash: "#connections" });
  }
  void loadServers();
  try {
    await factory.refreshConnections();
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
});
async function mcpAdded() {
  addingMcp.value = false;
  error.value = "";
  await loadServers();
}
</script>
<template>
  <section id="connections" class="settings-section">
    <div class="sec-head">
      <div>
        <h2>Connections</h2>
        <p>One account per provider. Every project on that provider uses it.</p>
      </div>
      <button
        v-if="session.isAdmin && !addingMcp"
        class="btn small"
        type="button"
        @click="addingMcp = true"
      >
        Add MCP server
      </button>
    </div>
    <p v-if="loadError" class="notice" role="alert">{{ loadError }}</p>
    <p
      v-if="oauthFlag === 'ok'"
      class="notice"
      role="status"
      data-test="mcp-oauth-flag"
    >
      Signed in. Discover the server's tools to choose which ones agents may
      use.
    </p>
    <p
      v-else-if="oauthFlag === 'error'"
      class="notice"
      role="alert"
      data-test="mcp-oauth-flag"
    >
      Sign-in did not complete: the link may have expired or the provider
      refused it. Check the server's error below and sign in again.
    </p>

    <div v-for="g in groups" :key="g.id" class="conn-group">
      <h3 class="sub">
        {{ g.name }} <span class="count">{{ g.note }}</span>
      </h3>
      <div class="card-list">
        <ProviderRow
          v-for="p in g.providers"
          :key="p.id"
          :provider="p"
          :credentials-configured="credentialsConfigured"
          @error="error = $event"
        />
        <p v-if="!g.providers.length" class="help">None yet.</p>
      </div>
    </div>

    <div class="conn-group">
      <h3 class="sub">
        MCP servers
        <span class="count">Extra tools for agents, admin only</span>
      </h3>
      <McpAddForm
        v-if="addingMcp"
        @added="mcpAdded"
        @cancel="addingMcp = false"
        @error="error = $event"
      />
      <div class="card-list">
        <McpServerRow
          v-for="s in servers"
          :key="s.id"
          :server="s"
          :can-manage="session.isAdmin"
          @changed="loadServers"
        />
        <p v-if="!servers.length && !addingMcp" class="help">None yet.</p>
      </div>
    </div>
    <p v-if="error" class="notice" role="alert">{{ error }}</p>
  </section>
</template>
