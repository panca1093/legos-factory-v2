<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Copy, HelpCircle, RefreshCw } from "lucide-vue-next";
import Popover from "../../components/ui/Popover.vue";
import {
  api,
  type AgentInfo,
  type AgentsResponse,
  type UsageSummary,
} from "../../types/api";
import type { Settings, SettingsPatch } from "../../types/api";
import AgentRow from "./AgentRow.vue";
import RolesTable from "./RolesTable.vue";

defineProps<{
  settings: Settings;
  editable: boolean;
  canWrite: boolean;
  credentialsConfigured: boolean;
}>();
const emit = defineEmits<{ saved: [SettingsPatch] }>();

const agents = ref<AgentInfo[]>([]);
const error = ref(""),
  loading = ref(true),
  help = ref(false);

const week = ref<Record<string, { tokens: number; runs: number }>>({});
async function loadWeek() {
  try {
    const tz = encodeURIComponent(
      Intl.DateTimeFormat().resolvedOptions().timeZone,
    );
    const res = await api.get<UsageSummary>(
      `/api/v1/usage/summary?days=7&tz=${tz}`,
    );
    week.value = Object.fromEntries(
      (res.accounts ?? []).map((a) => [
        `${a.agent}/${a.account_id}`,
        { tokens: a.tokens_in + a.tokens_out, runs: a.runs },
      ]),
    );
  } catch {
    week.value = {};
  }
}

async function load() {
  void loadWeek();
  try {
    const res = await api.get<AgentsResponse>("/api/v1/agents");
    agents.value = (res.agents ?? []).map((a) => ({
      ...a,
      accounts: a.accounts ?? [],
      roles_allowed: a.roles_allowed ?? [],
    }));
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}
onMounted(load);

const installed = computed(() => agents.value.filter((a) => a.installed));
const missing = computed(() => agents.value.filter((a) => !a.installed));

const copy = (text: string) => void navigator.clipboard?.writeText(text);
</script>
<template>
  <section id="agents" class="settings-section">
    <div class="sec-head">
      <div>
        <h2>Agents</h2>
        <p>
          Agents found on this machine and the account each one signs in with.
        </p>
      </div>
      <div class="sec-actions">
        <span class="scope personal">Yours · only you see these</span>
        <button class="btn small" type="button" @click="load">
          <RefreshCw class="icon" aria-hidden="true" /> Refresh
        </button>
      </div>
    </div>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <p v-if="loading" class="help" aria-busy="true">Looking for agents…</p>

    <h3 class="sub">
      Installed <span class="count">{{ installed.length }} detected</span>
    </h3>
    <div class="card-list">
      <AgentRow
        v-for="a in installed"
        :key="a.id"
        :agent="a"
        :can-write="canWrite"
        :credentials-configured="credentialsConfigured"
        :week="week"
        @changed="load"
        @error="error = $event"
      />
      <p v-if="!installed.length && !loading" class="help">
        No supported agent was found on this machine.
      </p>
    </div>

    <details v-if="missing.length" class="install">
      <summary>
        Available to install <span class="count">{{ missing.length }}</span>
      </summary>
      <div v-for="a in missing" :key="a.id" class="agent-acct">
        <h3>{{ a.name }}</h3>
        <div v-if="a.install_hint" class="cmd">
          <code>{{ a.install_hint }}</code><button
            class="btn small icon-only"
            type="button"
            :aria-label="`Copy install command for ${a.name}`"
            @click="copy(a.install_hint!)"
          >
            <Copy class="icon" aria-hidden="true" />
          </button>
        </div>
      </div>
    </details>

    <h3 class="sub">
      Roles
      <Popover v-model:open="help">
        <template #trigger="{ toggle, open }">
          <button
            class="btn small icon-only"
            type="button"
            aria-label="How roles pick an agent"
            :aria-expanded="open"
            @click="toggle"
          >
            <HelpCircle class="icon" aria-hidden="true" />
          </button>
        </template>
        <p class="pop-text" role="note">
          Each role runs on the agent and model you pick here. A work order can
          override the model when it is created; Foreman and Architect keep the
          model they started with. Permissions are fixed by role for safety,
          except Chat.
        </p>
      </Popover>
    </h3>
    <RolesTable
      :settings="settings"
      :agents="agents"
      :editable="editable"
      @saved="emit('saved', $event)"
    />
  </section>
</template>
