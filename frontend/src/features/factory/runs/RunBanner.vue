<script setup lang="ts">
import { computed } from "vue";
import { Pause, Play, SkipForward } from "lucide-vue-next";
import type { Run, Task, WorkOrder } from "../../../types/api";
import { stateInfo, taskState } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
const props = defineProps<{
  order: WorkOrder;
  tasks: Task[];
  runs: Run[];
  canWrite: boolean;
  busy: boolean;
}>();
const emit = defineEmits<{ pause: []; resume: [] }>();
const completed = computed(
  () =>
    props.tasks.filter((t) => taskState(t.id, props.runs) === "completed")
      .length,
);
const finished = computed(() =>
  ["READY", "CANCELLED", "BLOCKED"].includes(props.order.state),
);
const paused = computed(() => !!props.order.paused_at);
const guidedStep = computed(
  () => paused.value && props.order.pause_reason?.startsWith("Guided"),
);
</script>
<template>
  <div>
    <div class="order-summary">
      <BrickBot persona="foreman" class="compact" />
      <div class="grow">
        <strong class="small"
          >{{ stateInfo(order.state).label
          }}<span v-if="paused" class="pill orange" style="margin-left: 8px"
            >Paused</span
          ></strong
        >
        <p class="help">
          {{ completed }} of {{ tasks.length }} tasks completed ·
          {{ order.autonomy }} autonomy
        </p>
      </div>
      <div class="run-controls">
        <button
          v-if="!paused"
          class="btn small"
          data-test="execution-pause"
          :disabled="!canWrite || busy || finished"
          @click="emit('pause')"
        >
          <Pause class="icon" />Pause</button
        ><button
          v-else
          class="btn small primary"
          data-test="execution-step"
          :disabled="!canWrite || busy"
          @click="emit('resume')"
        >
          <component :is="guidedStep ? SkipForward : Play" class="icon" />{{
            guidedStep ? "Next step" : "Resume"
          }}
        </button>
      </div>
    </div>
    <p
      v-if="paused"
      class="notice"
      role="status"
      data-test="pause-reason"
      style="margin: -9px 0 20px"
    >
      {{ order.pause_reason || "Paused." }} Queued work waits; nothing is lost.
    </p>
    <p v-else class="help" style="margin: -9px 0 20px">
      Pause stops after the current step. Stop cancels the order.
    </p>
  </div>
</template>
