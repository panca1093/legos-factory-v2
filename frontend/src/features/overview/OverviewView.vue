<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { RouterLink } from "vue-router";
import {
  Plus,
  MessageSquare,
  Zap,
  CircleCheck,
  LayoutGrid,
  ArrowRight,
  Code,
  ChevronRight,
  TriangleAlert,
} from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import BrickBot from "../../components/agents/BrickBot.vue";
import StatusBadge from "../../components/ui/StatusBadge.vue";
import AppDialog from "../../components/ui/AppDialog.vue";
import {
  attentionStates,
  crew,
  isActive,
  money,
  projectColor,
  runningStates,
  type Persona,
} from "../../composables/presentation";
const factory = useFactory(),
  session = useSession();
const error = ref(""),
  loading = ref(false),
  selected = ref<Persona | null>(null);
const attention = computed(() =>
  factory.workOrders.filter((w) => attentionStates.includes(w.state)),
);
const running = computed(() =>
  factory.workOrders.filter((w) => runningStates.includes(w.state)),
);
const assembled = computed(() =>
  factory.workOrders.filter((w) => w.state === "READY"),
);
const now = new Date();
const hour = Number(
  new Intl.DateTimeFormat("en-US", {
    hour: "numeric",
    hourCycle: "h23",
    timeZone: "Asia/Jakarta",
  }).format(now),
);
const greeting =
  hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
const date = new Intl.DateTimeFormat("en-US", {
  weekday: "long",
  month: "long",
  day: "numeric",
  timeZone: "Asia/Jakarta",
}).format(now);
const name = computed(() => session.user?.name.split(" ").at(-1) ?? "there");
async function load() {
  loading.value = true;
  error.value = "";
  try {
    await Promise.all([
      factory.refreshOverview(),
      factory.refreshProjects(),
      factory.refreshWorkOrders(),
    ]);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}
onMounted(load);
function observed(role: string) {
  const e = factory.liveEvents.find((e) => e.actor.toLowerCase() === role);
  return e
    ? e.kind.replaceAll("_", " ")
    : role === "cataloger"
      ? "A future chapter"
      : "availability not reported";
}
</script>
<template>
  <div v-if="error" class="error-state" role="alert">
    {{ error }}
    <div><button class="btn small" @click="load">Try again</button></div>
  </div>
  <div class="welcome">
    <div>
      <div class="eyebrow">{{ date }} · Your workspace, at a glance</div>
      <h1 class="intro">
        {{ greeting }}, {{ name
        }}<span class="greeting-icon" aria-hidden="true">✳</span>
      </h1>
      <p class="subtitle">
        {{
          attention.length
            ? `${attention.length} decisions need you.`
            : "You’re all caught up."
        }}
        Your crew is keeping things moving.
      </p>
    </div>
    <RouterLink v-if="session.canWrite" class="btn primary" to="/factory?new=1"
      ><Plus class="icon" />New work order</RouterLink
    >
  </div>
  <section class="floor" aria-label="Your factory crew" :aria-busy="loading">
    <div class="floor-header row between">
      <div>
        <h2>A little crew. Big possibilities.</h2>
        <p class="muted small" style="margin-top: 4px">
          Five perspectives, one well-built outcome.
        </p>
      </div>
      <span class="pill green"
        ><i class="dot" :class="{ pulse: running.length }"></i
        >{{ running.length }} active</span
      >
    </div>
    <div class="crew-track">
      <div
        v-if="running.length"
        class="conveyor-brick"
        aria-hidden="true"
      ></div>
      <div
        v-for="c in crew"
        :key="c.id"
        class="crew-station"
        :class="{ future: c.id === 'cataloger' }"
      >
        <button :aria-label="`Inspect ${c.name}`" @click="selected = c.id">
          <BrickBot :persona="c.id" /><span class="station-name">{{
            c.name
          }}</span
          ><span class="station-status">{{ observed(c.id) }}</span>
        </button>
      </div>
    </div>
  </section>
  <section class="metrics" aria-label="Workspace monitoring">
    <div class="metric">
      <RouterLink to="/factory?status=attention"
        ><span class="metric-label"
          ><MessageSquare class="icon" />Needs you</span
        >
        <div class="metric-value">
          <b>{{ attention.length }}</b
          ><span>decisions waiting</span>
        </div></RouterLink
      >
    </div>
    <div class="metric">
      <RouterLink to="/factory?status=running"
        ><span class="metric-label"><Zap class="icon" />In the making</span>
        <div class="metric-value">
          <b>{{ running.length }}</b
          ><span>active work orders</span>
        </div></RouterLink
      >
    </div>
    <div class="metric">
      <RouterLink to="/factory?status=complete"
        ><span class="metric-label"><CircleCheck class="icon" />Assembled</span>
        <div class="metric-value">
          <b>{{ assembled.length }}</b
          ><span>completed orders</span>
        </div></RouterLink
      >
    </div>
    <div class="metric">
      <RouterLink to="/settings?tab=usage"
        ><span class="metric-label"><LayoutGrid class="icon" />My usage</span>
        <div class="metric-value">
          <b>{{ money(factory.overview?.usage.cost_micros) }}</b
          ><span>{{
            factory.overview?.budget.limit_micros
              ? "/ " + money(factory.overview.budget.limit_micros)
              : "No budget cap"
          }}</span>
        </div></RouterLink
      >
    </div>
  </section>
  <div class="overview-grid">
    <section>
      <div class="section-head">
        <h2>
          A human touch, please
          <span class="pill orange" style="margin-left: 5px">{{
            attention.length
          }}</span>
        </h2>
        <RouterLink class="link" to="/factory?status=attention"
          >See in Factory</RouterLink
        >
      </div>
      <article
        v-for="w in attention.slice(0, 2)"
        :key="w.id"
        class="attention-card"
      >
        <BrickBot
          :persona="
            w.state === 'WAITING_FOR_HUMAN' || w.state === 'ARCHITECT_REVIEW'
              ? 'architect'
              : 'inspector'
          "
          class="medium"
        />
        <div class="grow">
          <div class="row between">
            <span class="tiny muted"
              >{{ factory.projects.find((p) => p.id === w.project_id)?.name }} /
              {{ w.id }}</span
            ><StatusBadge :state="w.state" />
          </div>
          <h3>{{ w.title }}</h3>
          <p>
            {{
              w.state === "WAITING_FOR_HUMAN"
                ? "Architect needs a contract decision before the crew can build."
                : w.state === "ARCHITECT_REVIEW"
                  ? "Your blueprint is ready for review."
                  : w.state === "BLOCKED"
                    ? "Work is blocked. Inspect the recorded evidence."
                    : "The checks are in. Your crew is ready for a PR decision."
            }}
          </p>
          <RouterLink class="btn small" :to="`/factory/${w.id}`"
            ><ArrowRight class="icon" />{{
              w.state === "WAITING_FOR_HUMAN"
                ? "Answer clarification"
                : w.state === "ARCHITECT_REVIEW"
                  ? "Review blueprint"
                  : "Review evidence"
            }}</RouterLink
          >
        </div>
      </article>
      <div v-if="!attention.length" class="panel empty">
        No decisions waiting. The crew has what it needs.
      </div>
    </section>
    <section>
      <div class="section-head">
        <h2>Across your projects</h2>
        <RouterLink class="link" to="/factory">Open Factory</RouterLink>
      </div>
      <div class="panel" style="padding: 0 20px">
        <RouterLink
          v-for="(p, i) in factory.projects"
          :key="p.id"
          class="project-line"
          :to="{ path: '/factory', query: { project: p.id } }"
          style="color: var(--ink)"
          ><span class="project-box" :style="{ '--c': projectColor(i) }"
            ><Code class="icon" /></span
          ><span class="grow"
            ><strong class="small">{{ p.name }}</strong>
            <p>
              {{ p.language }} ·
              {{
                factory.workOrders.filter(
                  (w) => w.project_id === p.id && isActive(w),
                ).length
              }}
              active work orders
            </p></span
          ><span class="project-card-count" aria-label="Work order count">{{
            factory.workOrders.filter((w) => w.project_id === p.id).length
          }}</span
          ><ChevronRight class="icon"
        /></RouterLink>
        <div v-if="!factory.projects.length" class="empty">
          No projects yet.
          <RouterLink to="/factory">Add a project in Factory.</RouterLink>
        </div>
      </div>
    </section>
  </div>
  <div class="resource-strip">
    <span class="status-line"
      ><i
        class="dot"
        :style="{ color: factory.connected ? 'var(--green)' : 'var(--muted)' }"
      ></i
      ><strong>{{
        factory.connected ? "Live workspace updates" : "Workspace monitoring"
      }}</strong></span
    ><span
      >Pending / running jobs:
      {{ factory.overview?.metrics.pending_jobs ?? "—" }}</span
    ><span class="status-line"
      ><TriangleAlert
        v-if="factory.overview?.metrics.failed_jobs"
        class="icon"
      />Failed jobs: {{ factory.overview?.metrics.failed_jobs ?? "—" }}</span
    ><RouterLink class="grow" style="text-align: right" to="/settings?tab=usage"
      >Usage & resources</RouterLink
    >
  </div>
  <section class="section-gap">
    <div class="section-head">
      <h2>Recent activity</h2>
      <span class="tiny muted">Observed since this session started</span>
    </div>
    <div v-if="!factory.liveEvents.length" class="notice">
      No activity received yet. Open a work order to inspect its recorded
      history.
    </div>
    <div v-else class="panel panel-pad">
      <div
        v-for="(e, i) in factory.liveEvents.slice(0, 5)"
        :key="`${e.id}-${i}`"
        class="activity-item"
      >
        <span class="pill purple">{{ e.actor }}</span>
        <div class="grow">
          <p>{{ e.message }}</p>
          <RouterLink
            v-if="e.work_order_id"
            class="tiny"
            :to="`/factory/${e.work_order_id}`"
            >Open work order</RouterLink
          >
        </div>
      </div>
    </div>
  </section>
  <AppDialog
    :open="selected !== null"
    :title="crew.find((c) => c.id === selected)?.name ?? 'Agent'"
    @close="selected = null"
    ><div class="row">
      <BrickBot v-if="selected" :persona="selected" />
      <div>
        <h3>{{ crew.find((c) => c.id === selected)?.job }}</h3>
        <p class="help">{{ selected ? observed(selected) : "" }}</p>
      </div>
    </div>
    <div class="notice section-gap">
      {{
        selected === "cataloger"
          ? "Cataloger is an exploratory preview. No information is ingested."
          : "Availability and capacity are not reported by the backend. Activity shown here comes from observed workspace events."
      }}
    </div></AppDialog
  >
</template>
