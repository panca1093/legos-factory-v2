<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { FolderPlus, Plus } from "lucide-vue-next";
import "./projects.css";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import AddProjectDialog from "../factory/work-orders/AddProjectDialog.vue";
import NewWorkOrderDrawer from "../factory/work-orders/NewWorkOrderDrawer.vue";
import {
  isNeedsYou,
  isRunning,
  isShipped,
} from "../../composables/presentation";
import { isOpenOrder, repoLabel, woPath } from "./rail";
import type { Project } from "../../types/api";

const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter();
const adding = ref(false),
  error = ref(""),
  loading = ref(true);
const showNew = computed(() => route.query.new === "1");

const filter = computed(() =>
  route.query.filter === "attention" || route.query.filter === "shipped"
    ? route.query.filter
    : "",
);
interface Card {
  project: Project;
  open: number;
  needsYou: number;
  running: number;
  lastActivity: string;
  shipped: number;
}
const cards = computed<Card[]>(() =>
  factory.projects
    .map((project) => {
      const own = factory.workOrders.filter((w) => w.project_id === project.id);
      const open = own.filter(isOpenOrder);
      return {
        project,
        open: open.length,
        needsYou: open.filter(isNeedsYou).length,
        running: open.filter(isRunning).length,
        shipped: own.filter(isShipped).length,
        lastActivity: own.reduce(
          (m, w) => (w.updated_at > m ? w.updated_at : m),
          "",
        ),
      };
    })
    .filter((c) =>
      filter.value === "attention"
        ? c.needsYou > 0
        : filter.value === "shipped"
          ? c.shipped > 0
          : true,
    )
    .sort(
      (a, b) =>
        b.needsYou - a.needsYou ||
        b.lastActivity.localeCompare(a.lastActivity) ||
        a.project.name.localeCompare(b.project.name),
    ),
);
function ago(iso: string) {
  if (!iso) return "no activity yet";
  const m = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60000));
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  return m < 1440
    ? `${Math.round(m / 60)}h ago`
    : `${Math.round(m / 1440)}d ago`;
}
function closeNew() {
  const rest = { ...route.query };
  delete rest.new;
  void router.replace({ query: rest });
}
function created(r: { id: string; planningError: string }) {
  const w = factory.workOrders.find((o) => o.id === r.id);
  void router.push({
    path: w ? woPath(w.project_id, r.id) : "/projects",
    query: r.planningError ? { planningError: r.planningError } : {},
  });
}
onMounted(async () => {
  try {
    await Promise.all([factory.refreshProjects(), factory.refreshWorkOrders()]);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
});
</script>
<template>
  <div class="fx-page">
    <div class="page-heading">
      <div>
        <div class="eyebrow">Every repository you build in</div>
        <h1>Projects</h1>
        <p class="subtitle">
          Open a project to see its factory, pull requests and code.
        </p>
      </div>
      <div class="row">
        <button
          v-if="session.canWrite"
          class="lg-btn small"
          @click="adding = true"
        >
          <FolderPlus class="icon" />Add project</button
        ><button
          v-if="session.canWrite"
          class="lg-btn primary pill"
          :disabled="!factory.projects.length"
          @click="router.replace({ query: { ...route.query, new: '1' } })"
        >
          <Plus class="icon" />New work order
        </button>
      </div>
    </div>
    <p v-if="filter" class="notice row between" data-test="projects-filter">
      <span
        >Showing projects with
        {{
          filter === "attention" ? "decisions waiting on you" : "shipped work"
        }}.</span
      ><RouterLink to="/projects">Show all projects</RouterLink>
    </p>
    <div v-if="error" class="error-state" role="alert">{{ error }}</div>
    <div
      v-else-if="loading && !factory.projects.length"
      class="panel empty"
      aria-busy="true"
    >
      Loading projects…
    </div>
    <div v-else-if="!factory.projects.length" class="panel empty">
      <h3>Your factory starts with a project.</h3>
      <p class="help">Add a repository, then give your crew a clear outcome.</p>
      <button
        v-if="session.canWrite"
        class="lg-btn primary section-gap"
        @click="adding = true"
      >
        Add project
      </button>
    </div>
    <ul v-else class="pj-grid" aria-label="Projects">
      <li v-for="c in cards" :key="c.project.id">
        <RouterLink
          :to="`/projects/${c.project.id}`"
          class="lg-card pj-card"
          data-test="project-card"
        >
          <div class="pj-top">
            <strong>{{ c.project.name }}</strong>
            <span v-if="c.needsYou" class="fx-pill orange"
              >{{ c.needsYou }} needs you</span
            >
          </div>
          <small class="muted">{{ repoLabel(c.project) }}</small>
          <dl class="pj-stats">
            <div>
              <dt>Open</dt>
              <dd>{{ c.open }}</dd>
            </div>
            <div>
              <dt>Running</dt>
              <dd>{{ c.running }}</dd>
            </div>
          </dl>
          <small class="muted">Last activity {{ ago(c.lastActivity) }}</small>
        </RouterLink>
      </li>
    </ul>
  </div>
  <AddProjectDialog
    v-if="adding"
    :open="adding"
    @close="adding = false"
    @created="
      (id) => {
        adding = false;
        router.push(`/projects/${id}`);
      }
    "
  />
  <NewWorkOrderDrawer
    v-if="showNew"
    :open="showNew"
    @close="closeNew"
    @created="created"
  />
</template>
