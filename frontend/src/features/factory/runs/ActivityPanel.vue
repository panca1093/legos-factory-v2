<script setup lang="ts">
import { persona, money } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
import type { WOEvent, Run } from "../../../types/api";
defineProps<{ events: WOEvent[]; runs: Run[] }>();
</script>
<template>
  <div>
    <div class="section-head">
      <h2>What’s happening</h2>
      <span class="pill">Recorded event stream</span>
    </div>
    <TransitionGroup name="view" tag="div" class="timeline"
      ><article v-for="e in [...events].reverse()" :key="e.id" class="event">
        <BrickBot :persona="persona(e.actor)" class="compact" />
        <div class="event-body">
          <div class="row between">
            <strong class="small">{{
              e.actor.charAt(0).toUpperCase() + e.actor.slice(1)
            }}</strong
            ><time :datetime="e.created_at">{{
              new Date(e.created_at).toLocaleString()
            }}</time>
          </div>
          <p>{{ e.message }}</p>
          <span class="tiny muted">{{ e.kind.replaceAll("_", " ") }}</span>
        </div>
      </article></TransitionGroup
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
