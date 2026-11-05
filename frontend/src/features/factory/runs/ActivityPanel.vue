<script setup lang="ts">
import { computed } from "vue";
import { persona, isCrew, money } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
import type { WOEvent, Run, Task } from "../../../types/api";
import AgentLiveCard from "./AgentLiveCard.vue";
import AgentRunCard from "./AgentRunCard.vue";
import PlanProgress from "./PlanProgress.vue";
import { gateSummary, groupActivity, liveRuns } from "./agentActivity";
const props = withDefaults(
  defineProps<{
    events: WOEvent[];
    runs: Run[];
    tasks?: Task[];
    canRetry?: boolean;
    revision?: number;
    steer?: (runID: string, message: string) => Promise<unknown>;
  }>(),
  { tasks: () => [], canRetry: false, steer: undefined },
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
      <span class="pill">Recorded event stream</span>
    </div>
    <AgentLiveCard
      v-for="r in live"
      :key="r.id"
      :run="r"
      :task="taskTitle.get(r.taskID)"
      :steer="steer && ((m: string) => steer!(r.id, m))"
    />
    <TransitionGroup name="view" tag="div" class="timeline"
      ><template v-for="it in history"
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
