<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { Lock, TriangleAlert } from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import type { RuntimeInfo, Settings } from "../../types/api";
import "./settings.css";
import AdvancedSection from "./AdvancedSection.vue";
import AgentsSection from "./AgentsSection.vue";
import ConnectionsSection from "./ConnectionsSection.vue";
import ServersPanel from "../editor/ServersPanel.vue";

const factory = useFactory(),
  session = useSession(),
  route = useRoute();
const config = ref<Settings>({
  architect_model: "",
  builder_model: "",
  inspector_model: "",
  allowed_models: null,
});
const runtime = ref<RuntimeInfo | null>(null);
const loading = ref(true),
  initialized = ref(false),
  error = ref("");

const sections = [
  { id: "agents", label: "Agents" },
  { id: "connections", label: "Connections" },
  { id: "editor", label: "Editor" },
  { id: "advanced", label: "Advanced" },
];
const legacyTabs: Record<string, string> = {
  governance: "agents",
  integrations: "connections",
  usage: "agents",
};
const target = computed(() => {
  const hash = route.hash.replace(/^#/, "");
  return hash || legacyTabs[String(route.query.tab ?? "")] || "";
});
const current = ref("agents");
async function scrollToTarget() {
  await nextTick();
  if (!target.value) return;
  current.value = target.value;
  document.getElementById(target.value)?.scrollIntoView?.({ block: "start" });
}

async function load() {
  loading.value = true;
  error.value = "";
  const [settings, rt] = await Promise.allSettled([
    factory.loadSettings(),
    factory.loadRuntime(),
  ]);
  initialized.value = settings.status === "fulfilled";
  if (settings.status === "fulfilled") config.value = settings.value;
  if (rt.status === "fulfilled") runtime.value = rt.value;
  error.value = [settings, rt]
    .filter((r) => r.status === "rejected")
    .map((r) =>
      r.reason instanceof Error ? r.reason.message : String(r.reason),
    )
    .join(" · ");
  loading.value = false;
  void scrollToTarget();
}
onMounted(load);
watch(target, scrollToTarget);

function merge(patch: Partial<Settings>) {
  for (const [k, v] of Object.entries(patch))
    if (v !== undefined)
      (config.value as unknown as Record<string, unknown>)[k] = v;
}
const credentialsOK = computed(
  () => runtime.value?.credentials_configured ?? true,
);
</script>
<template>
  <div class="page-heading">
    <div>
      <h1>Settings</h1>
      <p class="subtitle">
        Agents and accounts are yours. Connections and Advanced apply to the
        whole workspace.
      </p>
    </div>
    <span class="pill purple">{{ session.user?.role }}</span>
  </div>
  <div class="settings-layout">
    <nav class="settings-nav" aria-label="Settings sections">
      <RouterLink
        v-for="s in sections"
        :key="s.id"
        :to="{ hash: `#${s.id}` }"
        :aria-current="current === s.id ? 'true' : undefined"
        @click="current = s.id"
        >{{ s.label }}</RouterLink
      >
    </nav>
    <div class="detail-content settings-main">
      <div v-if="loading" class="panel empty" aria-busy="true">
        Loading workspace settings…
      </div>
      <template v-else>
        <div v-if="error" class="error-state" role="alert">
          {{ error }}<button class="btn small" @click="load">Try again</button>
        </div>
        <p
          v-if="runtime && !credentialsOK"
          class="notice"
          data-test="secret-banner"
        >
          <TriangleAlert class="icon" aria-hidden="true" />
          <span
            ><b>Credentials can't be saved.</b> Set
            <code>FACTORY_SECRET_KEY</code> on the server and restart it. Until
            then you can't add accounts or connections.</span
          >
        </p>
        <p v-if="!session.isAdmin" class="notice" data-test="read-only-note">
          <Lock class="icon" aria-hidden="true" /> You can look but not change
          workspace defaults. Ask an admin to change them.
        </p>

        <AgentsSection
          :settings="config"
          :editable="session.isAdmin && initialized"
          :can-write="session.canWrite"
          :credentials-configured="credentialsOK"
          @saved="merge"
        />
        <ConnectionsSection :credentials-configured="credentialsOK" />
        <ServersPanel />
        <AdvancedSection
          :settings="config"
          :runtime="runtime"
          :editable="session.isAdmin && initialized"
          @saved="merge"
        />
      </template>
    </div>
  </div>
</template>
