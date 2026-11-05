<script setup lang="ts">
import { computed } from "vue";
import { Check } from "lucide-vue-next";
import type { Run, WOEvent, WorkOrder } from "../../../types/api";
import { stages, stageStatuses } from "../../../composables/presentation";
const props = defineProps<{
  order: WorkOrder;
  runs: Run[];
  events?: WOEvent[];
}>();
const emit = defineEmits<{ select: [number] }>();
const statuses = computed(() =>
  stageStatuses(props.order, props.runs, props.events),
);
const text = {
  done: "Done",
  cur: "In progress",
  wait: "Needs you",
  stop: "Stopped",
  todo: "Up next",
};
</script>
<template>
  <nav class="fx-track" aria-label="Work order stages" data-test="stage-track">
    <button
      v-for="(name, i) in stages"
      :key="name"
      class="fx-stage"
      :class="statuses[i]"
      :data-stage="name"
      :aria-current="
        ['cur', 'wait', 'stop'].includes(statuses[i]) ? 'step' : undefined
      "
      @click="emit('select', i)"
    >
      <span class="fx-stage-top"
        ><span class="num"
          ><Check v-if="statuses[i] === 'done'" class="icon" /><template
            v-else
            >{{ i + 1 }}</template
          ></span
        ><b>{{ name }}</b
        ><span class="st">{{
          i === 4 && statuses[i] === "done" ? "Merged" : text[statuses[i]]
        }}</span></span
      >
      <span class="fx-stage-bar"><i></i></span>
    </button>
  </nav>
</template>
