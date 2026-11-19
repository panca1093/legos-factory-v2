<script setup lang="ts">
import { computed, onMounted } from "vue";
import PanelCard from "../projects/PanelCard.vue";
import {
  checkServers,
  loadServers,
  lspState,
  restartLanguageServer,
  serverForLanguage,
  servers,
} from "./lsp";

const props = defineProps<{ language: string | null }>();
const id = computed(() => serverForLanguage(props.language));
const info = computed(() => servers.value.find((s) => s.id === id.value));
const conn = computed(() => (id.value ? lspState[id.value] : undefined));
const takenOver = computed(() => conn.value?.conn === "taken_over");
const state = computed(() => {
  const c = conn.value?.conn;
  if (c === "running") return "running";
  if (c === "crashed") return "crashed";
  return info.value?.state ?? "";
});
const labels: Record<string, string> = {
  found: "found",
  not_found: "not found",
  outdated: "found · outdated",
  running: "running",
  crashed: "crashed",
};
const stateLabel = computed(() => labels[state.value] ?? "unknown");
function copy(text: string) {
  void navigator.clipboard?.writeText(text);
}
onMounted(() => {
  if (!servers.value.length) void loadServers();
});
</script>
<template>
  <PanelCard id="language-server" title="Language server">
    <p v-if="!id" class="help" data-test="no-server">
      No language server for this file type.
    </p>
    <template v-else>
      <div v-if="takenOver" class="notice" data-test="taken-over">
        Opened in another tab ·
        <button
          type="button"
          class="btn small"
          @click="restartLanguageServer(id)"
        >
          Use here
        </button>
      </div>
      <p data-test="server-state" :data-state="state">
        <b>{{ id }}</b> · {{ stateLabel }}
        <span v-if="info?.version" class="mono"> {{ info.version }}</span>
      </p>
      <p v-if="info?.path" class="mono" data-test="server-path">
        {{ info.path }}
      </p>
      <p v-if="conn?.progress" class="help" data-test="server-progress">
        {{ conn.progress.title }} {{ conn.progress.message }}
        <template v-if="conn.progress.percentage != null">
          {{ conn.progress.percentage }}%
        </template>
      </p>
      <pre v-if="info?.error" class="mono" data-test="server-error">{{
        info.error
      }}</pre>
      <div
        v-if="info?.install && state !== 'running'"
        class="install"
        data-test="install"
      >
        <code>{{ info.install }}</code>
        <button type="button" class="btn small" @click="copy(info.install)">
          Copy
        </button>
      </div>
      <div class="actions">
        <button
          v-if="state === 'crashed' || state === 'running'"
          type="button"
          class="btn small"
          @click="restartLanguageServer(id)"
        >
          Restart
        </button>
        <button
          v-if="state === 'not_found' || state === 'outdated' || !state"
          type="button"
          class="btn small"
          @click="checkServers"
        >
          Check again
        </button>
        <RouterLink :to="{ path: '/settings', hash: '#editor' }"
          >Settings › Editor</RouterLink
        >
      </div>
    </template>
  </PanelCard>
</template>
<style scoped>
.install {
  display: flex;
  gap: 8px;
  align-items: center;
  margin: 6px 0;
}
.install code {
  flex: 1;
  overflow-wrap: anywhere;
  font-size: 11px;
}
.actions {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  font-size: 12px;
}
</style>
