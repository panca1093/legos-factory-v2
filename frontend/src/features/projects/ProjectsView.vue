<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { FolderPlus, Plus, Search } from "lucide-vue-next";
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
import "../factory/factory.css";
import type { Project } from "../../types/api";

const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter();
const adding = ref(false),
  error = ref(""),
  loading = ref(true);
const showNew = computed(() => route.query.new === "1");

const sort = ref<"attention" | "az">("attention"),
  search = ref("");
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
  shippedRecent: number;
}
const MONTH = 30 * 24 * 3600 * 1000;
const initials = (name: string) =>
  name
    .split(/[^a-zA-Z0-9]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0].toUpperCase())
    .join("") || "?";
const tone = (name: string) =>
  [...name].reduce((n, c) => n + c.charCodeAt(0), 0) % 4;
const provider = (p: Project) =>
  p.github_repo || /github/i.test(p.remote_repo ?? "")
    ? "GitHub"
    : /bitbucket/i.test(p.remote_repo ?? "")
      ? "Bitbucket"
      : p.remote_repo || p.git_connection_id
        ? "Remote"
        : "Local folder";
const footStatus = (c: Card) =>
  c.needsYou
    ? "Waiting on you"
    : c.running
      ? "Crew working"
      : c.shipped
        ? "All shipped"
        : "Idle";
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
        shippedRecent: own.filter(
          (w) =>
            isShipped(w) &&
            Date.now() - Date.parse(w.pr_merged_at ?? w.updated_at) < MONTH,
        ).length,
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
    .filter(
      (c) =>
        !search.value.trim() ||
        `${c.project.name} ${repoLabel(c.project)}`
          .toLowerCase()
          .includes(search.value.trim().toLowerCase()),
    )
    .sort((a, b) =>
      sort.value === "az"
        ? a.project.name.localeCompare(b.project.name)
        : b.needsYou - a.needsYou ||
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
        <h1>Projects</h1>
        <p class="subtitle">
          Each project is one door: its Factory, its pull requests and its
          code.
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
    <div v-if="factory.projects.length" class="pj-tools">
      <div class="fx-chips" role="group" aria-label="Sort projects">
        <button
          class="fx-chip"
          :class="{ on: sort === 'attention' }"
          :aria-pressed="sort === 'attention'"
          data-test="sort-attention"
          @click="sort = 'attention'"
        >
          Needs attention first
        </button>
        <button
          class="fx-chip"
          :class="{ on: sort === 'az' }"
          :aria-pressed="sort === 'az'"
          data-test="sort-az"
          @click="sort = 'az'"
        >
          A–Z
        </button>
      </div>
      <label class="pj-search"
        ><Search class="icon" /><input
          v-model="search"
          type="search"
          placeholder="Search projects"
          aria-label="Search projects"
          data-test="project-search"
      /></label>
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
    <p v-else-if="!cards.length" class="muted section-gap" data-test="projects-no-match">
      No projects match “{{ search.trim() }}”.
    </p>
    <ul v-else class="pj-grid" aria-label="Projects">
      <li v-for="c in cards" :key="c.project.id">
        <RouterLink
          :to="`/projects/${c.project.id}`"
          class="lg-card pj-card"
          data-test="project-card"
        >
          <div class="pj-top">
            <span class="pj-avatar" :class="`t${tone(c.project.name)}`">{{
              initials(c.project.name)
            }}</span>
            <div class="pj-id">
              <strong>{{ c.project.name }}</strong>
              <small class="mono muted">{{ repoLabel(c.project) }}</small>
            </div>
            <span v-if="c.needsYou" class="fx-pill orange"
              >{{ c.needsYou }} needs you</span
            >
          </div>
          <dl class="pj-stats">
            <div>
              <dd>{{ c.running }}</dd>
              <dt>WO running</dt>
            </div>
            <div>
              <dd>—</dd>
              <dt>PRs for you</dt>
            </div>
            <div>
              <dd>{{ c.shippedRecent }}</dd>
              <dt>shipped · 30d</dt>
            </div>
          </dl>
          <div class="pj-foot">
            <small class="muted"
              >{{ footStatus(c) }} · {{ ago(c.lastActivity) }}</small
            >
            <span class="fx-pill purple">{{ provider(c.project) }}</span>
          </div>
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
