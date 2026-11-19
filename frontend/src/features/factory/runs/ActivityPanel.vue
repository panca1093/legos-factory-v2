<script setup lang="ts">
import { computed } from "vue";
import type { WOEvent, Run, Task } from "../../../types/api";
import PlanProgressCard from "./PlanProgressCard.vue";
import HappeningCard from "./HappeningCard.vue";
import PanelCard from "../../projects/PanelCard.vue";
import { woPanelSlots } from "../../projects/woPanelSlots";
const props = withDefaults(
  defineProps<{
    events: WOEvent[];
    runs: Run[];
    tasks?: Task[];
    canRetry?: boolean;
    revision?: number;
    hasMore?: boolean;
    inPanel?: boolean;
    steer?: (runID: string, message: string) => Promise<unknown>;
  }>(),
  {
    tasks: () => [],
    canRetry: false,
    inPanel: false,
    steer: undefined,
    hasMore: false,
  },
);
const emit = defineEmits<{ retry: [taskID: string]; retryRemediation: [] }>();
const slot = computed(() => woPanelSlots.top.value);
const hasTasks = computed(() => props.tasks.length > 0);
</script>
<template>
  <Teleport v-if="inPanel && slot" :to="slot">
    <PanelCard v-if="hasTasks" id="plan-progress" title="Plan progress">
      <PlanProgressCard
        bare
        :tasks="tasks"
        :runs="runs"
        :can-retry="canRetry"
        :revision="revision"
        @retry="emit('retry', $event)"
        @retry-remediation="emit('retryRemediation')"
      />
    </PanelCard>
    <PanelCard id="happening" title="What’s happening">
      <HappeningCard
        bare
        :events="events"
        :runs="runs"
        :tasks="tasks"
        :has-more="hasMore"
        :steer="steer"
      />
    </PanelCard>
  </Teleport>
  <div v-else-if="!inPanel" class="fx-rcol">
    <PlanProgressCard
      v-if="hasTasks"
      :tasks="tasks"
      :runs="runs"
      :can-retry="canRetry"
      :revision="revision"
      @retry="emit('retry', $event)"
      @retry-remediation="emit('retryRemediation')"
    />
    <HappeningCard
      :events="events"
      :runs="runs"
      :tasks="tasks"
      :has-more="hasMore"
      :steer="steer"
    />
  </div>
</template>
<style scoped>
.fx-rcol {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
  align-content: start;
}
</style>
