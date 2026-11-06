<script setup lang="ts">
import { computed, ref } from "vue";
import { persona, isCrew, money } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
import type { WOEvent, Run, Task } from "../../../types/api";
import AgentLiveCard from "./AgentLiveCard.vue";
import AgentRunCard from "./AgentRunCard.vue";
import PlanProgress from "./PlanProgress.vue";
import {
  gateSummary,
  groupActivity,
  liveRuns,
  runCacheSplit,
} from "./agentActivity";
const props = withDefaults(
  defineProps<{
    events: WOEvent[];
    runs: Run[];
    tasks?: Task[];
    canRetry?: boolean;
    revision?: number;
    hasMore?: boolean;
    steer?: (runID: string, message: string) => Promise<unknown>;
  }>(),
  { tasks: () => [], canRetry: false, steer: undefined, hasMore: false },
);
const emit = defineEmits<{ retry: [taskID: string]; retryRemediation: [] }>();
// The graph already shows every task; the list stays only for retry and remediation.
const retryable = computed(() =>
  props.runs.some(
    (r) =>
      r.kind === "builder" &&
      (r.state.toLowerCase() === "failed" || r.task_id === null),
  ),
);
const items = computed(() => groupActivity(props.events));
const live = computed(() => liveRuns(items.value));
const history = computed(() =>
  items.value.filter((it) => !live.value.includes(it as never)).reverse(),
);
const filters = [
  { key: "all", label: "All" },
  { key: "code", label: "Code" },
  { key: "agents", label: "Agents" },
  { key: "jev", label: "Jev" },
  { key: "human", label: "You" },
] as const;
type FilterKey = (typeof filters)[number]["key"];
const filter = ref<FilterKey>("all");
const actorOf = (it: (typeof history.value)[number]) =>
  (it.type === "run" ? it.actor : it.event.actor).toLowerCase();
const matches = (actor: string, f: FilterKey) =>
  f === "all" ||
  (f === "code" && actor === "factory") ||
  (f === "agents" && isCrew(actor)) ||
  actor === f;
// A steer is folded into the agent's run, so "You" must also match runs you steered.
const steered = (it: (typeof history.value)[number]) =>
  it.type === "run" && it.steps.some((st) => !!st.message);
const shown = computed(() =>
  history.value.filter(
    (it) =>
      matches(actorOf(it), filter.value) ||
      (filter.value === "human" && steered(it)),
  ),
);
const taskTitle = computed(
  () => new Map(props.tasks.map((t) => [t.id, t.title] as const)),
);
</script>
<template>
  <div class="fx-card fx-activity">
    <PlanProgress
      v-if="tasks.length && retryable"
      :tasks="tasks"
      :runs="runs"
      :can-retry="canRetry"
      :revision="revision"
      @retry="emit('retry', $event)"
      @retry-remediation="emit('retryRemediation')"
    />
    <div class="section-head">
      <h2>What’s happening</h2>
      <span class="tiny muted" data-test="activity-count"
        >{{ shown.length }} of {{ history.length }} past events</span
      >
    </div>
    <div
      v-if="history.length"
      class="fx-chips fx-afilter"
      role="group"
      aria-label="Filter activity"
    >
      <button
        v-for="f in filters"
        :key="f.key"
        type="button"
        class="fx-chip"
        :class="{ on: filter === f.key }"
        :aria-pressed="filter === f.key"
        @click="filter = f.key"
      >
        {{ f.label }}
      </button>
    </div>
    <AgentLiveCard
      v-for="r in live"
      :key="r.id"
      :run="r"
      :task="taskTitle.get(r.taskID)"
      :steer="steer && ((m: string) => steer!(r.id, m))"
    />
    <TransitionGroup
      name="view"
      tag="div"
      class="timeline fx-tl"
      role="region"
      :tabindex="shown.length ? 0 : undefined"
      aria-label="Activity events"
      ><template v-for="it in shown"
        ><article v-if="it.type === 'run'" :key="it.id" class="event">
          <BrickBot :persona="persona(it.actor)" class="compact" />
          <div class="event-body"><AgentRunCard :run="it" /></div>
        </article>
        <article v-else :key="it.event.id" class="event">
          <BrickBot
            v-if="isCrew(it.event.actor)"
            :persona="persona(it.event.actor)"
            class="compact"
          />
          <span v-else class="avatar system" aria-hidden="true">⚙</span>
          <div class="event-body">
            <div class="row between">
              <strong class="small">{{
                it.event.actor.charAt(0).toUpperCase() + it.event.actor.slice(1)
              }}</strong
              ><time :datetime="it.event.created_at">{{
                new Date(it.event.created_at).toLocaleString()
              }}</time>
            </div>
            <p>{{ it.event.message }}</p>
            <div
              v-if="gateSummary(it.event)"
              class="row wrap"
              data-test="gate-event"
              style="gap: 8px; margin-top: 4px"
            >
              <span class="pill" :class="gateSummary(it.event)!.tone">{{
                gateSummary(it.event)!.outcome
              }}</span
              ><span class="tiny muted"
                >{{ gateSummary(it.event)!.gate }} · table row
                {{ gateSummary(it.event)!.row }}</span
              ><span v-if="gateSummary(it.event)!.note" class="small">{{
                gateSummary(it.event)!.note
              }}</span>
            </div>
            <span class="tiny muted">{{
              it.event.kind.replaceAll("_", " ")
            }}</span>
          </div>
        </article></template
      ></TransitionGroup
    >
    <p v-if="hasMore" class="tiny muted fx-more" data-test="activity-more">
      Earlier events are not shown.
    </p>
    <div v-if="history.length && !shown.length" class="empty">
      No events match this filter.
    </div>
    <div v-if="!events.length" class="empty">
      The activity trail starts when you draft a blueprint.
    </div>
    <details
      class="fx-runs"
      :open="runs.some((r) => r.state.toLowerCase() === 'failed')"
    >
      <summary class="fx-sum">
        <span>Execution runs</span>
        <span class="tiny muted">{{ runs.length }}</span>
      </summary>
      <p class="help">
        Harness, output and accounting are reported by the backend.
      </p>
      <details
        v-for="(r, i) in runs"
        :key="r.id"
        :open="i === runs.length - 1"
        class="section-gap"
      >
        <summary class="row wrap" style="cursor: pointer">
          <span
            class="pill"
            :class="r.state.toLowerCase() === 'failed' ? 'red' : 'purple'"
            >{{ r.state }}</span
          ><strong class="small">{{ r.kind }}</strong
          ><span class="pill">{{ r.harness }}</span
          ><span class="tiny muted"
            >{{ money(r.cost_micros) }} ·
            {{ r.tokens_in + r.tokens_out }} tokens</span
          ><span
            v-if="runCacheSplit(r)"
            class="tiny muted"
            data-test="run-cache"
            :title="'Cost split is an estimate from price weights; the agent reports only the total.'"
            >· {{ runCacheSplit(r)!.fresh }} fresh in ·
            {{ runCacheSplit(r)!.read }} cache read (≈{{
              money(runCacheSplit(r)!.readMicros)
            }}) · {{ runCacheSplit(r)!.write }} cache write (≈{{
              money(runCacheSplit(r)!.writeMicros)
            }})</span
          >
        </summary>
        <pre class="output terminal">{{
          r.error || r.output || "No output recorded yet."
        }}</pre>
      </details>
      <div v-if="!runs.length" class="empty">No runs yet.</div>
    </details>
  </div>
</template>
<style scoped>
.fx-afilter {
  margin: 0 0 var(--space-3);
}
.fx-tl {
  max-height: min(480px, 60dvh);
  overflow-x: hidden;
  overflow-y: auto;
  overscroll-behavior: contain;
  margin: 0 -8px;
  padding: 4px 8px 0;
}
.fx-more {
  margin: var(--space-2) 0 0;
}
.fx-tl:empty {
  display: none;
}
.fx-tl:focus-visible {
  outline: none;
  box-shadow: var(--shadow-focus);
  border-radius: var(--radius-md);
}
.fx-tl :deep(.view-leave-active) {
  position: absolute;
  visibility: hidden;
}
@media (max-width: 720px) {
  .fx-tl {
    overscroll-behavior: auto;
  }
}
</style>
