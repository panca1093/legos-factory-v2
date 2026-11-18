<script setup lang="ts">
import { CircleCheck } from "lucide-vue-next";
import type { WorkOrder, Task } from "../../../types/api";
import type { DocSection } from "../runs/activityThread";
defineProps<{
  section: DocSection;
  order: WorkOrder;
  tasks: Task[];
  text?: string;
}>();
</script>
<template>
  <p v-if="section === 'decision'" style="white-space: pre-wrap">
    {{ order.clarif_answer }}
  </p>
  <p v-else-if="section === 'outcome'" style="white-space: pre-wrap">
    {{ order.objective }}
  </p>
  <p v-else-if="section === 'brief'" style="white-space: pre-wrap">
    {{ text ?? order.brief }}
  </p>
  <p v-else-if="section === 'spec'" style="white-space: pre-wrap">
    {{ order.spec }}
  </p>
  <template v-else-if="section === 'acceptance'">
    <ul v-if="order.acceptance" class="check-list">
      <li
        v-for="(criterion, i) in order.acceptance
          .split(/\n|\.\s+/)
          .filter(Boolean)"
        :key="i"
      >
        <CircleCheck class="icon" /><span>{{ criterion }}</span>
      </li>
    </ul>
    <p v-else>No acceptance criteria recorded.</p>
  </template>
  <template v-else-if="section === 'tasks'">
    <div v-if="tasks.length" class="stack" style="gap: 14px">
      <div
        v-for="(t, i) in tasks"
        :key="t.id"
        class="row"
        style="align-items: start"
      >
        <span
          class="square"
          style="width: 25px; height: 25px; font-size: 11px; flex-shrink: 0"
          >{{ i + 1 }}</span
        >
        <div style="min-width: 0">
          <strong class="small">{{ t.title }}</strong>
          <p class="help">{{ t.objective }}</p>
          <p v-if="t.scope?.length" class="help">
            Scope: {{ t.scope.join(", ") }}
          </p>
          <p v-if="t.deps?.length" class="help">
            After:
            {{
              t.deps
                .map((d) => tasks.find((n) => n.id === d)?.title ?? d)
                .join(", ")
            }}
          </p>
        </div>
      </div>
    </div>
    <p v-else>
      {{
        order.state === "CREATED"
          ? "Draft a blueprint to see the task breakdown."
          : "The task plan will appear when planning is complete."
      }}
    </p>
  </template>
  <p v-else>
    Work follows the recorded outcome and acceptance contract. New ambiguity
    returns to the human. Verification evidence identifies its revision.
  </p>
</template>
