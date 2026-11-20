<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  onBeforeRouteLeave,
  onBeforeRouteUpdate,
  RouterLink,
  RouterView,
  useRoute,
  useRouter,
} from "vue-router";
import { api, type WorkOrder } from "../../types/api";
import { PanelRight, PanelRightClose } from "lucide-vue-next";
import "./projects.css";
import { useFactory } from "../../stores/factory";
import NewWorkOrderDrawer from "../factory/work-orders/NewWorkOrderDrawer.vue";
import ProjectDetailsCard from "./ProjectDetailsCard.vue";
import WorkOrderPanel from "./WorkOrderPanel.vue";
import PRPanel from "./pulls/PRPanel.vue";
import SidePanel from "./SidePanel.vue";
import { useReviews } from "../../stores/reviews";
import { readStored, woPath, writeStored } from "./rail";
import CodePanel from "../editor/CodePanel.vue";
import EditorDialogs from "../editor/EditorDialogs.vue";
import { useEditor } from "../editor/editorStore";

const factory = useFactory(),
  reviews = useReviews(),
  editor = useEditor(),
  route = useRoute(),
  router = useRouter();
const panelKey = "legos-project-panel";
const projectId = computed(() => String(route.params.id));
const project = computed(() =>
  factory.projects.find((p) => p.id === projectId.value),
);
const ready = ref(false),
  loadError = ref("");
const kind = computed(() =>
  route.name === "project-wo"
    ? "wo"
    : route.name === "project-pulls"
      ? "pulls"
      : route.name === "project-pull"
        ? "pull"
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
      pull: "Pull requests",
      code: "Code",
    })[kind.value],
);
const hasPanel = computed(
  () =>
    kind.value === "overview" ||
    kind.value === "wo" ||
    kind.value === "pull" ||
    kind.value === "code",
);
const narrow = () => window.innerWidth <= 1280;
const narrowQuery = window.matchMedia?.("(max-width: 1280px)");
const panelOpen = ref(false);
function initPanel() {
  const stored = readStored(panelKey);
  panelOpen.value = narrow() ? false : stored !== "0";
}
function togglePanel() {
  panelOpen.value = !panelOpen.value;
  if (!narrow()) writeStored(panelKey, panelOpen.value ? "1" : "0");
}
initPanel();
narrowQuery?.addEventListener("change", initPanel);
const showPanelEvent = () => {
  if (!panelOpen.value) panelOpen.value = true;
};
function warnUnsaved(e: BeforeUnloadEvent) {
  if (!editor.dirtyPaths().length) return;
  e.preventDefault();
  e.returnValue = "";
}
window.addEventListener("legos:show-panel", showPanelEvent);
window.addEventListener("beforeunload", warnUnsaved);
onBeforeUnmount(() => {
  narrowQuery?.removeEventListener("change", initPanel);
  window.removeEventListener("legos:show-panel", showPanelEvent);
  window.removeEventListener("beforeunload", warnUnsaved);
  editor.detach();
});
onBeforeRouteLeave(() => editor.confirmLeave());
onBeforeRouteUpdate((to) =>
  String(to.params.id) === projectId.value ? true : editor.confirmLeave(),
);
const revealChecks = computed(
  () => kind.value === "overview" && route.query.checks === "1",
);
watch(
  revealChecks,
  (r) => {
    if (r) panelOpen.value = true;
  },
  { immediate: true },
);
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
async function settle() {
  ready.value = false;
  loadError.value = "";
  try {
    await Promise.all([factory.refreshProjects(), factory.refreshWorkOrders()]);
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
  if (kind.value === "wo" && !project.value) {
    try {
      const res = await api.get<{ work_order: WorkOrder }>(
        `/api/v1/work-orders/${encodeURIComponent(String(route.params.wo))}`,
      );
      if (res.work_order.project_id !== projectId.value) {
        void router.replace({
          path: woPath(res.work_order.project_id, res.work_order.id),
          query: route.query,
        });
        return;
      }
    } catch {
      /* Not found: fall through to the project state below. */
    }
  }
  ready.value = true;
}
editor.attach(projectId.value);
watch(projectId, (id) => {
  editor.attach(id);
  if (!project.value) void settle();
});
onMounted(settle);
</script>
<template>
  <div
    v-if="ready && !project && loadError"
    class="error-state"
    role="alert"
    data-test="project-error"
  >
    <h2>We couldn’t load this project.</h2>
    <p class="small">{{ loadError }}</p>
    <button class="btn" data-test="project-retry" @click="settle">
      Try again
    </button>
  </div>
  <div
    v-else-if="ready && !project"
    class="panel empty"
    data-test="project-missing"
  >
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
          />{{
            showPanel
              ? "Hide panel"
              : kind === "pull" && reviews.pending.length
                ? `Show panel · ${reviews.pending.length} to decide`
                : "Show panel"
          }}
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
      <SidePanel
        v-if="kind === 'overview'"
        :tabs="[{ key: 'details', label: 'Details' }]"
        label="Project panel"
      >
        <template #details>
          <ProjectDetailsCard :project="project" :reveal="revealChecks" />
        </template>
      </SidePanel>
      <PRPanel v-else-if="kind === 'pull'" />
      <SidePanel
        v-else-if="kind === 'code'"
        :tabs="[{ key: 'explorer', label: 'Explorer' }]"
        label="Code panel"
      >
        <template #explorer><CodePanel /></template>
      </SidePanel>
      <WorkOrderPanel v-else />
    </aside>
  </div>
  <EditorDialogs />
  <NewWorkOrderDrawer
    v-if="showNew"
    :open="showNew"
    :project-i-d="projectId"
    @close="closeNew"
    @created="created"
  />
</template>
