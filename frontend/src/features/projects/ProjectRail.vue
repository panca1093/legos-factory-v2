<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { ArrowLeft, ChevronRight, PanelLeftClose, Plus } from "lucide-vue-next";
import "./projects.css";
import { api, ApiError, type ProjectRail } from "../../types/api";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import IconButton from "../../components/ui/IconButton.vue";
import UsageCard from "../../app/layouts/UsageCard.vue";
import {
  capRail,
  railDot,
  readFlags,
  repoLabel,
  sortRail,
  woPath,
  writeStored,
} from "./rail";

defineEmits<{ hide: [] }>();
const route = useRoute(),
  router = useRouter(),
  factory = useFactory(),
  session = useSession();
const sectionsKey = "legos-project-rail-sections";
const projectId = computed(() => String(route.params.id ?? ""));
const selectedWO = computed(() => String(route.params.wo ?? ""));
const rail = ref<ProjectRail | null>(null),
  missing = ref(false),
  failed = ref(false),
  more = ref<Record<string, boolean>>({}),
  collapsed = ref<Record<string, boolean>>(readFlags(sectionsKey));
let seq = 0,
  timer: ReturnType<typeof setTimeout> | undefined;

const project = computed(() =>
  factory.projects.find((p) => p.id === projectId.value),
);
const name = computed(
  () => rail.value?.project?.name ?? project.value?.name ?? "",
);
const repo = computed(
  () =>
    rail.value?.project?.repo ||
    (project.value ? repoLabel(project.value) : ""),
);
const rows = computed(() => sortRail(rail.value?.work_orders?.rows ?? []));
const open = computed(() => rail.value?.work_orders?.open ?? rows.value.length);
const needsYou = computed(() => rows.value.some((r) => r.waiting_on_you));
const factoryRows = computed(() =>
  capRail(rows.value, !!more.value.factory, selectedWO.value),
);
const isOpen = (key: string) => !collapsed.value[key];
function toggle(key: string) {
  collapsed.value = { ...collapsed.value, [key]: isOpen(key) };
  writeStored(sectionsKey, JSON.stringify(collapsed.value));
}

async function load(part: "all" | "wo" = "all") {
  const id = projectId.value,
    mine = ++seq;
  try {
    const res = await api.get<ProjectRail>(
      `/api/v1/projects/${id}/rail?part=${part}`,
    );
    if (mine !== seq) return;
    rail.value =
      part === "all" || !rail.value
        ? res
        : { ...rail.value, project: res.project, work_orders: res.work_orders };
    missing.value = failed.value = false;
  } catch (e) {
    if (mine !== seq) return;
    missing.value = e instanceof ApiError && e.status === 404;
    failed.value = !missing.value;
    if (missing.value) rail.value = null;
  }
}
function schedule() {
  clearTimeout(timer);
  timer = setTimeout(() => void load("wo"), 1000);
}
watch(
  projectId,
  () => {
    clearTimeout(timer);
    rail.value = null;
    missing.value = failed.value = false;
    more.value = {};
    void load();
  },
  { immediate: true },
);
watch(
  () => factory.liveEvents[0],
  (ev) => {
    if (!ev) return;
    const wo = factory.workOrders.find((w) => w.id === ev.work_order_id);
    if (!wo || wo.project_id === projectId.value) schedule();
  },
);
onBeforeUnmount(() => {
  clearTimeout(timer);
  seq++;
});
const newOrder = () =>
  void router.replace({ query: { ...route.query, new: "1" } });
</script>
<template>
  <div class="pr" tabindex="-1">
    <div class="rail-head">
      <RouterLink to="/projects" class="pr-back"
        ><ArrowLeft class="icon" />All projects</RouterLink
      >
      <IconButton label="Hide sidebar" @click="$emit('hide')"
        ><PanelLeftClose class="icon"
      /></IconButton>
    </div>
    <div class="pr-id">
      <div class="pr-name">
        <strong>{{ missing ? "Project not found" : name }}</strong><i
          v-if="needsYou"
          class="pr-attn"
          title="Waiting on you"
          aria-label="Waiting on you"
        ></i>
      </div>
      <small v-if="repo && !missing" class="pr-repo">{{ repo }}</small>
    </div>
    <nav class="pr-nav grow" aria-label="Project">
      <p v-if="missing" class="empty-note">
        This project does not exist or you do not have access.
      </p>
      <template v-else>
        <section class="pr-sec">
          <div class="pr-sec-head">
            <button
              type="button"
              class="pr-chev"
              :aria-expanded="isOpen('factory')"
              aria-label="Toggle Factory section"
              @click="toggle('factory')"
            >
              <ChevronRight class="icon" />
            </button>
            <RouterLink
              :to="`/projects/${projectId}`"
              class="pr-title"
              data-test="rail-factory"
              >Factory · {{ open }} open</RouterLink
            >
            <IconButton
              v-if="session.canWrite"
              label="Add a work order"
              data-test="rail-new"
              @click="newOrder"
              ><Plus class="icon"
            /></IconButton>
          </div>
          <div v-if="isOpen('factory')" class="pr-rows">
            <RouterLink
              v-for="r in factoryRows.shown"
              :key="r.id"
              :to="woPath(projectId, r.id)"
              class="pr-row"
              :class="{ active: r.id === selectedWO }"
              :aria-current="r.id === selectedWO ? 'page' : undefined"
              data-test="rail-wo"
              ><i class="pr-dot" :class="railDot(r)" aria-hidden="true"></i
              ><span class="t">{{ r.title }}</span
              ><span v-if="r.waiting_on_you" class="pr-you"
                >you</span
              ></RouterLink
            >
            <p v-if="!rows.length && !failed" class="empty-note">
              {{ rail ? "No open work orders." : "Loading…" }}
            </p>
            <p v-if="failed" class="empty-note" role="alert">
              Could not load work orders.
            </p>
            <button
              v-if="factoryRows.hidden || more.factory"
              type="button"
              class="rt-more"
              @click="more = { ...more, factory: !more.factory }"
            >
              {{
                more.factory ? "Show less" : `Show ${factoryRows.hidden} more`
              }}
            </button>
          </div>
        </section>
        <section class="pr-sec">
          <div class="pr-sec-head">
            <button
              type="button"
              class="pr-chev"
              :aria-expanded="isOpen('pulls')"
              aria-label="Toggle Pull requests section"
              @click="toggle('pulls')"
            >
              <ChevronRight class="icon" />
            </button>
            <RouterLink :to="`/projects/${projectId}/pulls`" class="pr-title"
              >Pull requests</RouterLink
            >
          </div>
          <div v-if="isOpen('pulls')" class="pr-rows">
            <p class="pr-muted" data-test="rail-pulls">Coming soon</p>
          </div>
        </section>
        <section class="pr-sec">
          <div class="pr-sec-head">
            <button
              type="button"
              class="pr-chev"
              :aria-expanded="isOpen('code')"
              aria-label="Toggle Code section"
              @click="toggle('code')"
            >
              <ChevronRight class="icon" />
            </button>
            <RouterLink :to="`/projects/${projectId}/code`" class="pr-title"
              >Code</RouterLink
            >
          </div>
          <div v-if="isOpen('code')" class="pr-rows">
            <p class="pr-muted">Editor coming soon</p>
          </div>
        </section>
      </template>
    </nav>
    <UsageCard />
  </div>
</template>
