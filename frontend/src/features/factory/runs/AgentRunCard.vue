<script setup lang="ts">
import { computed } from "vue";
import { ChevronRight } from "lucide-vue-next";
import { duration, toolLine, type AgentRunGroup } from "./agentActivity";
const props = defineProps<{ run: AgentRunGroup }>();
const name = computed(
  () => props.run.actor.charAt(0).toUpperCase() + props.run.actor.slice(1),
);
const steps = computed(() => props.run.steps.length);
</script>
<template>
  <details class="agent-run" data-test="agent-run">
    <summary>
      <ChevronRight class="icon agent-run-chevron" />
      <strong class="small">{{ name }}</strong>
      <span class="tiny muted"
        >{{ steps }} step{{ steps === 1 ? "" : "s" }} ·
        {{ duration(run.startedAt, Date.parse(run.lastAt)) }} ·
        <time :datetime="run.lastAt">{{
          new Date(run.lastAt).toLocaleTimeString([], {
            hour: "2-digit",
            minute: "2-digit",
          })
        }}</time></span
      >
      <span class="pill" :class="run.status === 'failed' ? 'red' : 'green'">{{
        run.status === "failed" ? "Failed" : "Done"
      }}</span>
    </summary>
    <div class="agent-transcript">
      <template v-for="s in run.steps" :key="s.n"
        ><p v-for="(t, i) in s.tools" :key="`${s.n}-${i}`">
          {{ toolLine(t)
          }}<span v-if="t.failed" class="agent-failed"> · failed</span>
        </p></template
      >
      <p v-if="!steps" class="muted">Answered without using any tools.</p>
      <p v-if="run.outcome" class="agent-failed">{{ run.outcome }}</p>
    </div>
  </details>
</template>
