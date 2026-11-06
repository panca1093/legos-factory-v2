<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { RouterLink } from "vue-router";
import {
  Plus,
  ShieldCheck,
  Zap,
  Rocket,
  Gauge,
  Factory,
} from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import BrickBot from "../../components/agents/BrickBot.vue";
import StatusBadge from "../../components/ui/StatusBadge.vue";
import {
  money,
  isRunning,
  isTodo,
  needsYou,
  persona,
} from "../../composables/presentation";
import { heatCells, type HeatMode } from "./heatmap";
import "./overview.css";
const factory = useFactory(),
  session = useSession();
const mode = ref<HeatMode>("Daily"),
  heatScroll = ref<HTMLElement>();
const heatLabel: Record<HeatMode, string> = {
  Daily: "Agent runs per day",
  Weekly: "Agent runs per week, each week summed",
  Cumulative: "Cumulative agent runs per day",
};
const modes: HeatMode[] = ["Daily", "Weekly", "Cumulative"];
const approvals = computed(() => needsYou(factory.workOrders));
const running = computed(() => factory.workOrders.filter(isRunning));
const inMaking = computed(() =>
  factory.workOrders.filter((w) => isTodo(w) || isRunning(w)),
);
const shipped = computed(
  () => factory.workOrders.filter((w) => w.pr_merged_at).length,
);
const mtd = computed(() => factory.usage?.month_to_date);
const activity = computed(() => factory.usage?.activity ?? []);
const heat = computed(() => heatCells(activity.value, mode.value));
const totalRuns = computed(() =>
  activity.value.reduce((sum, d) => sum + d.count, 0),
);
const activeDays = computed(
  () => activity.value.filter((d) => d.count > 0).length,
);
const roles = computed(() =>
  [...(factory.usage?.roles ?? [])].sort((a, b) => b.runs - a.runs),
);
const compact = new Intl.NumberFormat("en-US", { notation: "compact" });
const tokens = (t: { tokens_in: number; tokens_out: number }) =>
  compact.format(t.tokens_in + t.tokens_out);
const now = new Date();
const hour = now.getHours();
const greeting =
  hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
const date = new Intl.DateTimeFormat("en-US", {
  weekday: "long",
  month: "long",
  day: "numeric",
}).format(now);
const name = computed(() => session.user?.name.split(" ").at(-1) ?? "there");
const projectName = (id: string) =>
  factory.projects.find((p) => p.id === id)?.name ?? "";
const action = (state: string) =>
  state === "WAITING_FOR_HUMAN"
    ? "Answer clarification"
    : state === "ARCHITECT_REVIEW"
      ? "Review blueprint"
      : state === "BLOCKED"
        ? "Review evidence"
        : "Review PR decision";
const scrollToLatest = () =>
  nextTick(() => {
    if (heatScroll.value)
      heatScroll.value.scrollLeft = heatScroll.value.scrollWidth;
  });
watch(() => heat.value.weeks, scrollToLatest);
onMounted(() => {
  if (factory.usage) void factory.refreshUsage();
});
</script>
<template>
  <div class="hero">
    <div>
      <div class="small muted">{{ date }}</div>
      <h1 class="greet">{{ greeting }}, {{ name }}</h1>
      <p class="muted">
        <b>{{
          approvals.length
            ? `${approvals.length} ${approvals.length > 1 ? "things need" : "thing needs"}`
            : "Nothing needs"
        }}</b>
        your OK.
        {{
          running.length
            ? `${running.length} work ${running.length > 1 ? "orders are" : "order is"} running.`
            : "No work orders are running."
        }}
      </p>
    </div>
    <RouterLink
      v-if="session.canWrite"
      class="lg-btn primary pill hero-cta"
      to="/factory?new=1"
      ><Plus class="icon" />New work order</RouterLink
    >
  </div>
  <section class="kpis" aria-label="Workspace summary">
    <RouterLink class="lg-card lg-stat" to="/factory?status=attention"
      ><div class="label"><ShieldCheck class="icon" />Needs you</div>
      <div class="value">{{ approvals.length }}</div>
      <div class="note">decisions waiting</div></RouterLink
    >
    <RouterLink class="lg-card lg-stat" to="/factory"
      ><div class="label"><Zap class="icon" />In the making</div>
      <div class="value">{{ inMaking.length }}</div>
      <div class="note">active work orders</div></RouterLink
    >
    <RouterLink class="lg-card lg-stat" to="/factory?status=complete"
      ><div class="label"><Rocket class="icon" />Shipped</div>
      <div class="value">{{ shipped }}</div>
      <div class="note">merged pull requests</div></RouterLink
    >
    <RouterLink class="lg-card lg-stat" to="/settings?tab=usage"
      ><div class="label"><Gauge class="icon" />This month</div>
      <div class="value">
        {{ mtd ? money(mtd.cost_micros) : "—" }}
      </div>
      <div class="note">
        {{
          mtd ? `${tokens(mtd)} tokens · ${mtd.runs} runs` : "Usage unavailable"
        }}
      </div></RouterLink
    >
  </section>
  <div class="grid2">
    <section class="lg-card" aria-labelledby="needs-you">
      <div class="lg-card-head">
        <h2 id="needs-you">Needs your approval</h2>
        <span class="lg-count">{{ approvals.length }}</span>
      </div>
      <div class="box">
        <div v-for="w in approvals" :key="w.id" class="lg-row">
          <div class="grow">
            <div class="title">{{ w.title }}</div>
            <div class="meta">{{ w.id }} · {{ projectName(w.project_id) }}</div>
          </div>
          <StatusBadge :state="w.state" />
          <RouterLink
            class="lg-btn small"
            :class="{ primary: w.state === 'WAITING_FOR_HUMAN' }"
            :to="`/factory/${w.id}`"
            >{{ action(w.state) }}</RouterLink
          >
        </div>
        <p v-if="!approvals.length" class="empty-note">
          All clear. Nothing waits on you.
        </p>
      </div>
    </section>
    <section class="lg-card" aria-labelledby="running-now">
      <div class="lg-card-head">
        <h2 id="running-now">Running now</h2>
        <RouterLink class="lg-btn ghost small" to="/factory"
          ><Factory class="icon" />Open Factory</RouterLink
        >
      </div>
      <div class="box">
        <RouterLink
          v-for="w in running"
          :key="w.id"
          class="lg-row runrow"
          :to="`/factory/${w.id}`"
        >
          <div class="grow">
            <div class="title">{{ w.title }}</div>
            <div class="meta">{{ w.id }} · {{ projectName(w.project_id) }}</div>
          </div>
          <StatusBadge :state="w.state" />
        </RouterLink>
        <p v-if="!running.length" class="empty-note">
          Nothing running. Start a work order from the Factory.
        </p>
      </div>
    </section>
  </div>
  <div class="grid31">
    <section class="lg-card act-card" aria-labelledby="user-activity">
      <div class="lg-card-head">
        <div>
          <h2 id="user-activity">User activity</h2>
          <div class="small muted">
            <template v-if="totalRuns"
              ><b>{{ totalRuns.toLocaleString() }}</b> agent runs in the last
              year · active on {{ activeDays }} days</template
            ><template v-else>Agent runs from your work orders</template>
          </div>
        </div>
        <div class="mode-seg" role="group" aria-label="Activity view">
          <button
            v-for="m in modes"
            :key="m"
            type="button"
            :class="{ active: mode === m }"
            :aria-pressed="mode === m"
            @click="mode = m"
          >
            {{ m }}
          </button>
        </div>
      </div>
      <p v-if="!factory.usage" class="empty-note">
        Usage unavailable.
        <button class="lg-btn ghost small" @click="factory.refreshUsage()">
          Try again
        </button>
      </p>
      <p v-else-if="!totalRuns" class="empty-note">
        No agent runs yet. Activity appears once your crew starts working.
      </p>
      <template v-else>
        <p class="sr-only">
          {{ totalRuns }} agent runs in the last year, active on
          {{ activeDays }} days.
        </p>
        <div ref="heatScroll" class="heat-scroll">
          <div
            class="heat"
            :style="{
              gridTemplateColumns: `repeat(${heat.weeks}, minmax(10px, 1fr))`,
            }"
            role="img"
            :aria-label="heatLabel[mode]"
          >
            <i
              v-for="c in heat.cells"
              :key="c.date"
              :class="`h${c.level}`"
              :style="{ gridRow: c.row + 1, gridColumn: c.col + 1 }"
              :title="c.label"
            ></i>
          </div>
          <div
            class="heat-months"
            :style="{
              gridTemplateColumns: `repeat(${heat.weeks}, minmax(10px, 1fr))`,
            }"
            aria-hidden="true"
          >
            <span
              v-for="m in heat.months"
              :key="m.col"
              :style="{ gridColumn: m.col + 1 }"
              >{{ m.label }}</span
            >
          </div>
        </div>
        <div class="heat-legend" aria-hidden="true">
          <span>Less</span><i v-for="l in 5" :key="l" :class="`h${l - 1}`"></i
          ><span>More</span>
        </div>
      </template>
    </section>
    <section class="lg-card agents-card" aria-labelledby="your-agents">
      <div class="lg-card-head">
        <h2 id="your-agents">Your agents</h2>
        <span class="lg-count">{{ roles.length }}</span>
      </div>
      <div class="agents">
        <div v-for="r in roles" :key="r.role" class="agent-row">
          <BrickBot :persona="persona(r.role)" :size="30" />
          <div class="an">
            <b>{{ r.role.charAt(0).toUpperCase() + r.role.slice(1) }}</b>
            <span class="aj">{{ r.runs }} runs · {{ tokens(r) }} tokens</span>
          </div>
          <span class="cost">{{ money(r.cost_micros) }}</span>
        </div>
        <p v-if="!roles.length" class="empty-note">
          {{ factory.usage ? "No agent usage yet." : "Usage unavailable." }}
        </p>
      </div>
      <div class="small muted foot">Last 12 months</div>
    </section>
  </div>
</template>
