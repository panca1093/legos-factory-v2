<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "../../types/api";
import {
  checkServers,
  loadServers,
  servers,
  serversError,
  type ServerInfo,
} from "./lsp";

const busy = ref(false),
  error = ref("");
const editing = ref<string | null>(null),
  path = ref(""),
  confirming = ref(false);

function copy(text: string) {
  void navigator.clipboard?.writeText(text);
}
async function check() {
  busy.value = true;
  await checkServers();
  busy.value = false;
}
function startEdit(id: string) {
  editing.value = id;
  path.value = "";
  confirming.value = false;
  error.value = "";
}
function review() {
  if (!path.value.trim().startsWith("/")) {
    error.value = "Enter an absolute path, e.g. /usr/local/bin/gopls.";
    return;
  }
  error.value = "";
  confirming.value = true;
}
async function confirm() {
  busy.value = true;
  try {
    const updated = await api.put<ServerInfo>(
      `/api/v1/editor/servers/${editing.value}`,
      { path: path.value.trim(), confirm: true },
    );
    servers.value = servers.value.map((s) =>
      s.id === updated.id ? updated : s,
    );
    editing.value = null;
    confirming.value = false;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    confirming.value = false;
  } finally {
    busy.value = false;
  }
}
const stateLabel = (s: ServerInfo) =>
  ({
    found: "found",
    not_found: "not found",
    outdated: "found · outdated",
    running: "running",
    crashed: "crashed",
  })[s.state] ?? s.state;
onMounted(loadServers);
</script>
<template>
  <section id="editor" class="settings-section">
    <div class="sec-head">
      <div>
        <h2>Editor</h2>
        <p>
          Language servers the Code editor can start. Legos finds them on your
          PATH.
        </p>
      </div>
      <button class="btn small" type="button" :disabled="busy" @click="check">
        Check again
      </button>
    </div>
    <p v-if="error || serversError" class="notice" role="alert">
      {{ error || serversError }}
    </p>
    <div class="card-list">
      <div
        v-for="s in servers"
        :key="s.id"
        class="provider"
        data-test="server-row"
      >
        <div class="agent-head">
          <div class="grow">
            <h3>
              {{ s.id }} <span class="mono">{{ s.languages.join(", ") }}</span>
            </h3>
            <p data-test="row-state">
              {{ stateLabel(s)
              }}<template v-if="s.version"> · {{ s.version }}</template>
              <template v-if="s.path">
                · <span class="mono">{{ s.path }}</span></template
              >
            </p>
          </div>
          <button class="btn small" type="button" @click="startEdit(s.id)">
            Use a different binary…
          </button>
        </div>
        <pre v-if="s.error" class="mono">{{ s.error }}</pre>
        <div
          v-if="s.install && s.state !== 'found' && s.state !== 'running'"
          data-test="install"
        >
          <code>{{ s.install }}</code>
          <button class="btn small" type="button" @click="copy(s.install)">
            Copy
          </button>
        </div>
        <form v-if="editing === s.id" @submit.prevent="review">
          <label :for="`path-${s.id}`">Path to the binary</label>
          <input
            :id="`path-${s.id}`"
            v-model="path"
            type="text"
            :disabled="confirming"
          />
          <button v-if="!confirming" class="btn small" type="submit">
            Review
          </button>
          <button class="btn small" type="button" @click="editing = null">
            Cancel
          </button>
        </form>
        <div
          v-if="editing === s.id && confirming"
          class="notice"
          data-test="confirm"
        >
          Legos will run <code>{{ path.trim() }} version</code> once to check
          it, then use it for {{ s.id }}.
          <button
            class="btn small"
            type="button"
            :disabled="busy"
            data-test="confirm-btn"
            @click="confirm"
          >
            Confirm
          </button>
          <button class="btn small" type="button" @click="confirming = false">
            Back
          </button>
        </div>
      </div>
    </div>
  </section>
</template>
