<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import {
  Plus,
  Search,
  LayoutGrid,
  Columns3,
  List,
  Code,
  Folder,
  ArrowRight,
} from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import StatusBadge from "../../components/ui/StatusBadge.vue";
import BrickBot from "../../components/agents/BrickBot.vue";
import AppDialog from "../../components/ui/AppDialog.vue";
import NewWorkOrderDrawer from "./work-orders/NewWorkOrderDrawer.vue";
import NewProjectDialog from "./work-orders/NewProjectDialog.vue";
import {
  attentionStates,
  runningStates,
  projectColor,
} from "../../composables/presentation";
const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  router = useRouter();
const search = ref(""),
  view = ref<"board" | "list">("board"),
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
const columns = [
  {
    name: "To shape",
    states: [
      "CREATED",
      "REQUIREMENT_ANALYSIS",
      "TASK_DECOMPOSITION",
      "SPEC_AND_PLAN_GENERATION",
      "ARCHITECT_REVIEW",
      "WAITING_FOR_HUMAN",
      "PLANNING",
    ],
    color: "var(--orange)",
  },
  {
    name: "In the making",
    states: ["QUEUED", "IMPLEMENTING"],
    color: "var(--violet)",
  },
  {
    name: "Quality & decisions",
    states: [
      "VERIFYING",
      "SPECIALIST_REVIEW",
      "PR_PREPARATION",
      "MERGE_READINESS",
      "NEEDS_HUMAN_REVIEW",
      "BLOCKED",
    ],
    color: "var(--green)",
  },
  { name: "Assembled", states: ["READY", "CANCELLED"], color: "var(--muted)" },
];
const filtered = computed(() =>
  factory.workOrders.filter(
    (w) =>
      (projectID.value === "all" || w.project_id === projectID.value) &&
      (status.value === "all" ||
        (status.value === "attention"
          ? attentionStates.includes(w.state)
          : status.value === "running"
            ? runningStates.includes(w.state)
            : status.value === "complete"
              ? w.state === "READY"
              : w.state === status.value)) &&
      (w.title + " " + w.objective + " " + w.id)
        .toLowerCase()
        .includes(search.value.toLowerCase()),
  ),
);
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
  <div class="page-heading">
    <div>
      <div class="eyebrow">One place for every project</div>
      <h1>The factory floor</h1>
      <p class="subtitle">From a good idea to a change you can trust.</p>
    </div>
    <div class="row">
      <button
        v-if="session.canWrite"
        class="btn small"
        @click="newProject = true"
      >
        <Folder class="icon" />New project</button
      ><button
        v-if="session.canWrite"
        class="btn primary"
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
  <div class="project-tabs" aria-label="Filter by project">
    <button
      class="project-chip"
      :class="{ active: projectID === 'all' }"
      @click="setQuery('project', 'all')"
    >
      <LayoutGrid class="icon" />All projects<span class="pill">{{
        factory.workOrders.length
      }}</span></button
    ><button
      v-for="(p, i) in factory.projects"
      :key="p.id"
      class="project-chip"
      :class="{ active: projectID === p.id }"
      @click="setQuery('project', p.id)"
    >
      <i class="project-dot" :style="{ '--c': projectColor(i) }"></i
      >{{ p.name }}
    </button>
  </div>
  <div v-if="selected" class="scenario">
    <span
      class="project-box"
      :style="{ '--c': projectColor(factory.projects.indexOf(selected)) }"
      ><Code class="icon"
    /></span>
    <div class="grow">
      <strong>{{ selected.name }}</strong>
      <p>{{ selected.repo_url }} · {{ selected.language }}</p>
    </div>
    <button class="btn small" @click="profile = true">
      <Folder class="icon" />Project profile
    </button>
  </div>
  <div class="toolbar">
    <div class="input-wrap">
      <Search class="icon" /><input
        v-model="search"
        aria-label="Filter work orders"
        placeholder="Filter work orders…"
      />
    </div>
    <select
      aria-label="Filter by status"
      :value="status"
      @change="setQuery('status', ($event.target as HTMLSelectElement).value)"
    >
      <option value="all">All statuses</option>
      <option value="attention">Needs you</option>
      <option value="running">In progress</option>
      <option value="complete">Assembled</option>
      <option value="BLOCKED">Blocked</option>
      <option value="CREATED">Draft</option>
      <option value="CANCELLED">Closed</option></select
    ><span class="grow"></span
    ><span class="tiny muted">{{ filtered.length }} work orders</span>
    <div class="segmented">
      <button
        aria-label="Board view"
        :aria-pressed="view === 'board'"
        :class="{ active: view === 'board' }"
        @click="view = 'board'"
      >
        <Columns3 class="icon" /></button
      ><button
        aria-label="List view"
        :aria-pressed="view === 'list'"
        :class="{ active: view === 'list' }"
        @click="view = 'list'"
      >
        <List class="icon" />
      </button>
    </div>
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
      class="btn primary section-gap"
      @click="newProject = true"
    >
      New project
    </button>
  </div>
  <template v-else
    ><div v-if="!filtered.length" class="notice" style="margin-bottom: 18px">
      No work orders match. Adjust the filters or create a new work order.
    </div>
    <div v-if="view === 'board'" class="board">
      <section
        v-for="column in columns"
        :key="column.name"
        class="board-column"
      >
        <div class="column-head">
          <div class="row" style="gap: 7px">
            <i class="dot" :style="{ color: column.color }"></i
            >{{ column.name }}
          </div>
          <span class="count">{{
            filtered.filter((w) => column.states.includes(w.state)).length
          }}</span>
        </div>
        <RouterLink
          v-for="w in filtered.filter((w) => column.states.includes(w.state))"
          :key="w.id"
          :to="`/factory/${w.id}`"
          class="order-card"
          ><div class="card-project">
            <i
              class="project-dot"
              :style="{
                '--c': projectColor(
                  factory.projects.findIndex((p) => p.id === w.project_id),
                ),
              }"
            ></i
            >{{ factory.projects.find((p) => p.id === w.project_id)?.name
            }}<span>{{ w.id.slice(0, 12) }}</span>
          </div>
          <h3>{{ w.title }}</h3>
          <StatusBadge :state="w.state" />
          <div class="card-footer">
            <div class="row" style="gap: 4px">
              <BrickBot persona="builder" class="compact" /><BrickBot
                persona="inspector"
                class="compact"
              /><span>{{ w.autonomy }}</span>
            </div>
            <span>rev {{ w.revision }}</span>
          </div></RouterLink
        >
        <div
          v-if="!filtered.some((w) => column.states.includes(w.state))"
          class="column-empty"
        >
          Room for the next piece.
        </div>
      </section>
    </div>
    <div v-else class="panel" style="overflow: auto">
      <table class="work-table">
        <thead>
          <tr>
            <th>Work order</th>
            <th>Status</th>
            <th>Project</th>
            <th>Autonomy</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="w in filtered" :key="w.id">
            <td>
              <RouterLink :to="`/factory/${w.id}`"
                ><strong>{{ w.title }}</strong>
                <p class="tiny muted">{{ w.id }}</p></RouterLink
              >
            </td>
            <td><StatusBadge :state="w.state" /></td>
            <td>
              {{ factory.projects.find((p) => p.id === w.project_id)?.name }}
            </td>
            <td>{{ w.autonomy }}</td>
            <td>
              <RouterLink
                :to="`/factory/${w.id}`"
                :aria-label="`Open ${w.title}`"
                ><ArrowRight class="icon"
              /></RouterLink>
            </td>
          </tr>
        </tbody>
      </table></div
  ></template>
  <div class="resource-strip">
    <span class="status-line"
      ><i class="dot" style="color: var(--muted)"></i
      ><strong>Workspace monitoring</strong></span
    ><span>Host capacity is not reported</span
    ><RouterLink class="grow" style="text-align: right" to="/settings?tab=usage"
      >Usage & resources</RouterLink
    >
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
        <Code class="icon" /><strong>{{ selected.repo_url }}</strong>
      </div>
      <p class="small muted">
        {{ selected.language }} ·
        {{
          selected.description ||
          "Work orders use this repository and its backend quality profile."
        }}
      </p>
      <div class="notice">
        Repository profile: {{ selected.language }}. Custom quality-profile
        editing is unavailable.
      </div>
    </div></AppDialog
  >
</template>
