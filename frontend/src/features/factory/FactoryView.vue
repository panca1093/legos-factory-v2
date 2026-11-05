<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { Plus, Folder, X } from "lucide-vue-next";
import "./factory.css";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import AppDialog from "../../components/ui/AppDialog.vue";
import Popover from "../../components/ui/Popover.vue";
import SearchField from "../../components/ui/SearchField.vue";
import ActorBadge from "./ActorBadge.vue";
import NewWorkOrderDrawer from "./work-orders/NewWorkOrderDrawer.vue";
import NewProjectDialog from "./work-orders/NewProjectDialog.vue";
import {
  actorOf,
  isFinished,
  orderStatus,
  projectColor,
  stageStatuses,
  stages,
  isNeedsYou,
  isRunning,
} from "../../composables/presentation";
import type { WorkOrder } from "../../types/api";
const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter();
const search = ref(""),
  help = ref(false),
  newProject = ref(false),
  profile = ref(false),
  error = ref(""),
  loading = ref(false);
const projectID = computed(() =>
  typeof route.query.project === "string" ? route.query.project : "all",
);
const status = computed(() =>
  typeof route.query.status === "string" ? route.query.status : "all",
);
const selected = computed(() =>
  factory.projects.find((p) => p.id === projectID.value),
);
const showNew = computed(() => route.query.new === "1");
const filtered = computed(() =>
  factory.workOrders.filter(
    (w) =>
      (projectID.value === "all" || w.project_id === projectID.value) &&
      (w.title + " " + w.objective + " " + w.id)
        .toLowerCase()
        .includes(search.value.toLowerCase()),
  ),
);
const groups = computed(() =>
  [
    {
      key: "attention",
      name: "Needs you",
      items: filtered.value.filter((w) => !isFinished(w) && isNeedsYou(w)),
    },
    {
      key: "running",
      name: "Running",
      items: filtered.value.filter(isRunning),
    },
    {
      key: "complete",
      name: "Finished",
      items: filtered.value.filter(isFinished),
    },
  ]
    .filter((g) => status.value === "all" || g.key === status.value)
    .map((g) =>
      status.value === "complete"
        ? { ...g, items: g.items.filter((w) => w.pr_merged_at) }
        : g,
    ),
);
const projectName = (w: WorkOrder) =>
  factory.projects.find((p) => p.id === w.project_id)?.name ?? "";
const color = (w: WorkOrder) =>
  projectColor(factory.projects.findIndex((p) => p.id === w.project_id));
function ago(iso: string) {
  const m = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60000));
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  return m < 1440
    ? `${Math.round(m / 60)}h ago`
    : `${Math.round(m / 1440)}d ago`;
}
function setQuery(key: string, value: string) {
  void router.replace({
    query: { ...route.query, [key]: value === "all" ? undefined : value },
  });
}
async function load() {
  loading.value = true;
  error.value = "";
  try {
    await Promise.all([factory.refreshProjects(), factory.refreshWorkOrders()]);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}
function created(result: { id: string; planningError: string }) {
  void router.push({
    path: `/factory/${result.id}`,
    query: result.planningError ? { planningError: result.planningError } : {},
  });
}
onMounted(load);
</script>
<template>
  <div class="fx-page">
    <div class="page-heading">
      <div>
        <div class="eyebrow">One place for every project</div>
        <h1>
          The factory floor
          <Popover v-model:open="help">
            <template #trigger="{ toggle }"
              ><button
                class="fx-help"
                aria-label="What do the stages and badges mean?"
                aria-haspopup="true"
                :aria-expanded="help"
                @click="toggle"
              >
                ?
              </button></template
            >
            <div class="fx-explain" @click.stop>
              <b>Five stages</b>
              <p class="small muted">
                Brief → Plan → Build → Review → Ship. Ship is done only when the
                pull request is merged.
              </p>
              <b>Who is acting</b>
              <ul>
                <li><ActorBadge actor="factory" small />Code: the factory</li>
                <li><ActorBadge actor="foreman" small />Agents</li>
                <li><ActorBadge actor="jev" small />Jev: judgment</li>
                <li><ActorBadge actor="you" small />You</li>
              </ul>
            </div>
          </Popover>
        </h1>
        <p class="subtitle">From a good idea to a change you can trust.</p>
      </div>
      <div class="row">
        <button
          v-if="session.canWrite"
          class="lg-btn small"
          @click="newProject = true"
        >
          <Folder class="icon" />New project</button
        ><button
          v-if="session.canWrite"
          class="lg-btn primary pill"
          :disabled="!factory.projects.length"
          @click="setQuery('new', '1')"
        >
          <Plus class="icon" />New work order
        </button>
      </div>
    </div>
    <div v-if="error" class="error-state" role="alert">
      {{ error }}
      <div><button class="btn small" @click="load">Try again</button></div>
    </div>
    <div class="fx-filters">
      <div class="fx-chips" role="group" aria-label="Filter by project">
        <button
          class="fx-chip"
          :class="{ on: projectID === 'all' }"
          :aria-pressed="projectID === 'all'"
          @click="setQuery('project', 'all')"
        >
          All projects
        </button>
        <button
          v-for="(p, i) in factory.projects"
          :key="p.id"
          class="fx-chip"
          :class="{ on: projectID === p.id }"
          :aria-pressed="projectID === p.id"
          @click="setQuery('project', p.id)"
        >
          <i class="fx-dot" :style="{ '--c': projectColor(i) }"></i>{{ p.name }}
        </button>
        <button
          v-if="status !== 'all'"
          class="fx-chip on"
          aria-label="Clear status filter"
          @click="setQuery('status', 'all')"
        >
          {{ groups[0]?.name }}<X class="icon" />
        </button>
      </div>
      <button
        v-if="selected"
        class="lg-btn small ghost"
        @click="profile = true"
      >
        Project profile
      </button>
      <SearchField
        v-model="search"
        label="Filter work orders"
        placeholder="Filter work orders…"
      />
    </div>
    <div
      v-if="loading && !factory.workOrders.length"
      class="panel empty"
      aria-busy="true"
    >
      Loading your factory…
    </div>
    <div v-else-if="!factory.projects.length" class="panel empty">
      <h3>Your factory starts with a project.</h3>
      <p class="help">Add a repository, then give your crew a clear outcome.</p>
      <button
        v-if="session.canWrite"
        class="lg-btn primary section-gap"
        @click="newProject = true"
      >
        New project
      </button>
    </div>
    <template v-else>
      <p v-if="!filtered.length" class="notice">
        No work orders match. Adjust the filters or create a new work order.
      </p>
      <section v-for="g in groups" :key="g.key" class="fx-group">
        <h2>
          {{ g.name }} <span class="fx-count">{{ g.items.length }}</span>
        </h2>
        <RouterLink
          v-for="w in g.items"
          :key="w.id"
          :to="`/factory/${w.id}`"
          class="fx-order"
          :class="{ attn: g.key === 'attention' }"
          data-test="order-row"
        >
          <span class="mono muted tiny">{{ w.id.slice(0, 12) }}</span>
          <div class="fx-order-main">
            <strong>{{ w.title }}</strong>
            <span class="tiny muted"
              ><i class="fx-dot" :style="{ '--c': color(w) }"></i
              >{{ projectName(w) }} · updated {{ ago(w.updated_at) }}</span
            >
          </div>
          <div
            class="fx-segs"
            role="img"
            :aria-label="
              stages.map((s, i) => `${s}: ${stageStatuses(w)[i]}`).join(', ')
            "
          >
            <i v-for="(st, i) in stageStatuses(w)" :key="i" :class="st"></i>
          </div>
          <span class="fx-pill" :class="orderStatus(w).tone">{{
            orderStatus(w).label
          }}</span>
          <ActorBadge :actor="actorOf(w)" />
        </RouterLink>
        <p v-if="!g.items.length" class="fx-empty">Nothing here.</p>
      </section>
    </template>
  </div>
  <NewWorkOrderDrawer
    v-if="showNew"
    :open="showNew"
    :project-i-d="projectID === 'all' ? '' : projectID"
    @close="setQuery('new', 'all')"
    @created="created"
  />
  <NewProjectDialog
    v-if="newProject"
    :open="newProject"
    @close="newProject = false"
    @created="
      (id) => {
        newProject = false;
        setQuery('project', id);
      }
    "
  />
  <AppDialog
    :open="profile"
    :title="selected?.name ?? 'Project'"
    @close="profile = false"
    ><div v-if="selected" class="stack">
      <div class="row">
        <strong>{{ selected.repo_url }}</strong>
      </div>
      <p class="small muted">
        {{ selected.language || "Language not detected yet" }} ·
        {{
          selected.description ||
          "Work orders use this repository and its backend quality profile."
        }}
      </p>
      <div class="notice">
        Checks are detected from the repository (go.mod, package.json,
        Cargo.toml). Add .legos/checks.json to the repository to override them.
      </div>
    </div></AppDialog
  >
</template>
