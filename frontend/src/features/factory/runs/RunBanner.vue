<script setup lang="ts">
import { computed } from "vue";
import { Pause, Play, SkipForward } from "lucide-vue-next";
import type { Run, Task } from "../../../types/api";
import { stateInfo, taskState } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
const props = defineProps<{ state: string; tasks: Task[]; runs: Run[] }>();
const completed = computed(
  () =>
    props.tasks.filter((t) => taskState(t.id, props.runs) === "completed")
      .length,
);
</script>
<template>
  <div>
    <div class="order-summary">
      <BrickBot persona="foreman" class="compact" />
      <div class="grow">
        <strong class="small">{{ stateInfo(state).label }}</strong>
        <p class="help">
          {{ completed }} of {{ tasks.length }} tasks completed · backend
          execution
        </p>
      </div>
      <div class="run-controls">
        <button
          class="btn small"
          data-test="execution-pause"
          disabled
          title="Pause/resume is unavailable through the backend API"
        >
          <Pause class="icon" />Pause</button
        ><button
          class="btn small primary"
          data-test="execution-step"
          disabled
          title="Manual stepping is unavailable through the backend API"
        >
          <SkipForward class="icon" />Next step</button
        ><button
          class="btn small text"
          disabled
          title="Auto-play is unavailable through the backend API"
        >
          <Play class="icon" />Auto-play
        </button>
      </div>
    </div>
    <p class="help" style="margin: -9px 0 20px">
      Manual execution controls are unavailable. The backend owns scheduling and
      execution; status reflects recorded task runs.
    </p>
  </div>
</template>
