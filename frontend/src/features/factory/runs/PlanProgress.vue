<script setup lang="ts">
import { computed } from "vue";
import { RotateCcw } from "lucide-vue-next";
import type { Run, Task } from "../../../types/api";
import { taskState } from "../../../composables/presentation";
const props = defineProps<{
  tasks: Task[];
  runs: Run[];
  canRetry: boolean;
  revision?: number;
}>();
const emit = defineEmits<{ retry: [taskID: string]; retryRemediation: [] }>();
const label: Record<string, string> = {
  waiting: "Waiting",
  running: "Building",
  completed: "Merged",
  failed: "Failed",
};
const pill: Record<string, string> = {
  running: "purple",
  completed: "green",
  failed: "red",
};
const rows = computed(() =>
  [...props.tasks]
    .sort((a, b) => a.seq - b.seq)
    .map((t) => {
      const state = taskState(t.id, props.runs);
      const error = props.runs.filter((r) => r.task_id === t.id).at(-1)?.error;
      return { task: t, state, error: state === "failed" ? error : "" };
    }),
);
const remediation = computed(() => {
  const run = props.runs
    .filter((r) => r.task_id === null && r.kind === "builder")
    .at(-1);
  if (!run) return null;
  const state = run.state.toLowerCase();
  return { state, error: state === "failed" ? run.error : "" };
});
const done = computed(
  () => rows.value.filter((r) => r.state === "completed").length,
);
</script>
<template>
  <section class="plan-progress" data-test="plan-progress">
    <div class="section-head">
      <h2>Plan progress</h2>
      <span class="pill">{{ done }} / {{ tasks.length }} merged</span>
    </div>
    <ol class="plan-tasks">
      <li v-for="r in rows" :key="r.task.id" :data-task="r.task.id">
        <div class="row between">
          <strong class="small">{{ r.task.title }}</strong>
          <span class="row" style="gap: 8px"
            ><span class="pill" :class="pill[r.state]">{{
              label[r.state] ?? r.state
            }}</span
            ><button
              v-if="r.state === 'failed' && canRetry"
              class="btn small"
              data-test="retry-task"
              @click="emit('retry', r.task.id)"
            >
              <RotateCcw class="icon" />Retry
            </button></span
          >
        </div>
        <p v-if="r.error" class="tiny agent-failed">{{ r.error }}</p>
      </li>
      <li v-if="remediation" data-test="remediation">
        <div class="row between">
          <strong class="small">{{
            revision ? `Remediation (revision ${revision})` : "Remediation"
          }}</strong>
          <span class="row" style="gap: 8px"
            ><span class="pill" :class="pill[remediation.state]">{{
              label[remediation.state] ?? remediation.state
            }}</span
            ><button
              v-if="remediation.state === 'failed' && canRetry"
              class="btn small"
              data-test="retry-remediation"
              @click="emit('retryRemediation')"
            >
              <RotateCcw class="icon" />Retry
            </button></span
          >
        </div>
        <p v-if="remediation.error" class="tiny agent-failed">
          {{ remediation.error }}
        </p>
      </li>
    </ol>
  </section>
</template>
<style scoped>
.plan-progress {
  margin-bottom: 24px;
}
.plan-tasks {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 8px;
}
.plan-tasks li {
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 8px 12px;
}
.plan-tasks p {
  margin: 4px 0 0;
}
</style>
