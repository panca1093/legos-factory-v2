<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { RouterLink, RouterView, useRoute, useRouter } from "vue-router";
import { PanelRight, PanelRightClose } from "lucide-vue-next";
import "./projects.css";
import { useFactory } from "../../stores/factory";
import NewWorkOrderDrawer from "../factory/work-orders/NewWorkOrderDrawer.vue";
import ProjectDetailsCard from "./ProjectDetailsCard.vue";
import WorkOrderPanel from "./WorkOrderPanel.vue";
import { readStored, woPath, writeStored } from "./rail";

const factory = useFactory(),
  route = useRoute(),
  router = useRouter();
const panelKey = "legos-project-panel";
const projectId = computed(() => String(route.params.id));
const project = computed(() =>
  factory.projects.find((p) => p.id === projectId.value),
);
const ready = ref(false);
const kind = computed(() =>
  route.name === "project-wo"
    ? "wo"
    : route.name === "project-pulls"
      ? "pulls"
      : route.name === "project-code"
        ? "code"
        : "overview",
);
const label = computed(
  () =>
    ({
      overview: "Factory",
      wo: "Factory",
      pulls: "Pull requests",
      code: "Code",
    })[kind.value],
);
const hasPanel = computed(
  () => kind.value === "overview" || kind.value === "wo",
);
const narrow = () => window.innerWidth < 1280;
const panelOpen = ref(false);
function initPanel() {
  const stored = readStored(panelKey);
  panelOpen.value = narrow() ? false : stored !== "0";
}
function togglePanel() {
  panelOpen.value = !panelOpen.value;
  if (!narrow()) writeStored(panelKey, panelOpen.value ? "1" : "0");
}
const showPanel = computed(() => hasPanel.value && panelOpen.value);
const showNew = computed(() => route.query.new === "1");

function closeNew() {
  const rest = { ...route.query };
  delete rest.new;
  void router.replace({ query: rest });
}
function created(r: { id: string; planningError: string }) {
  void router.push({
    path: woPath(projectId.value, r.id),
    query: r.planningError ? { planningError: r.planningError } : {},
  });
}
watch(
  () => factory.detail?.work_order,
  (wo) => {
    if (
      wo &&
      kind.value === "wo" &&
      wo.id === route.params.wo &&
      wo.project_id !== projectId.value
    )
      void router.replace({
        path: woPath(wo.project_id, wo.id),
        query: route.query,
      });
  },
);
watch(
  () => route.fullPath,
  () => {
    if (narrow() && panelOpen.value) panelOpen.value = false;
  },
);
onMounted(async () => {
  initPanel();
  try {
    await Promise.all([factory.refreshProjects(), factory.refreshWorkOrders()]);
  } catch {
    /* The rail and the page report their own load errors. */
  }
  ready.value = true;
});
</script>
<template>
  <div v-if="ready && !project" class="panel empty" data-test="project-missing">
    <h2>We couldn’t find that project.</h2>
    <p class="help">
      It may have been deleted, or you may not have access to it.
    </p>
    <RouterLink class="lg-btn primary section-gap" to="/projects"
      >Back to projects</RouterLink
    >
  </div>
  <div v-else class="pw" :class="{ 'panel-open': showPanel }">
    <div class="pw-main">
      <div class="pw-bar">
        <span class="lg-kind">{{ label }}</span>
        <button
          type="button"
          class="lg-btn small ghost"
          data-test="panel-toggle"
          :disabled="!hasPanel"
          :aria-pressed="showPanel"
          aria-controls="pw-panel"
          @click="togglePanel"
        >
          <PanelRightClose v-if="showPanel" class="icon" /><PanelRight
            v-else
            class="icon"
          />{{ showPanel ? "Hide panel" : "Show panel" }}
        </button>
      </div>
      <RouterView />
    </div>
    <div v-if="showPanel" class="pw-scrim" @click="togglePanel"></div>
    <aside
      v-if="showPanel && project"
      id="pw-panel"
      class="pw-panel"
      aria-label="Context"
    >
      <ProjectDetailsCard v-if="kind === 'overview'" :project="project" />
      <WorkOrderPanel v-else />
    </aside>
  </div>
  <NewWorkOrderDrawer
    v-if="showNew"
    :open="showNew"
    :project-i-d="projectId"
    @close="closeNew"
    @created="created"
  />
</template>
