<script setup lang="ts">
import { computed } from "vue";
import { persona, money } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
import type { WOEvent, Run } from "../../../types/api";
import AgentLiveCard from "./AgentLiveCard.vue";
import AgentRunCard from "./AgentRunCard.vue";
import { groupActivity, liveRun } from "./agentActivity";
const props = defineProps<{ events: WOEvent[]; runs: Run[] }>();
const items = computed(() => groupActivity(props.events));
const live = computed(() => liveRun(items.value));
const history = computed(() =>
  items.value.filter((it) => it !== live.value).reverse(),
);
</script>
<template>
  <div>
    <div class="section-head">
      <h2>What’s happening</h2>
      <span class="pill">Recorded event stream</span>
    </div>
    <AgentLiveCard v-if="live" :key="live.id" :run="live" />
    <TransitionGroup name="view" tag="div" class="timeline"
      ><template v-for="it in history"
        ><article v-if="it.type === 'run'" :key="it.id" class="event">
          <BrickBot :persona="persona(it.actor)" class="compact" />
          <div class="event-body"><AgentRunCard :run="it" /></div>
        </article>
        <article v-else :key="it.event.id" class="event">
          <BrickBot :persona="persona(it.event.actor)" class="compact" />
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
    <div class="section-head section-gap"><h2>Execution runs</h2></div>
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
  </div>
</template>
