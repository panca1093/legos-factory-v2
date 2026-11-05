<script setup lang="ts">
import { Download, CircleCheck } from "lucide-vue-next";
import type { WorkOrder, Task } from "../../../types/api";
defineProps<{ order: WorkOrder; tasks: Task[] }>();
const emit = defineEmits<{ export: [] }>();
</script>
<template>
  <div>
    <section
      v-if="order.clarif_answer"
      class="plan-section"
      data-test="decision"
    >
      <h3>Decision recorded</h3>
      <p style="white-space: pre-wrap">{{ order.clarif_answer }}</p>
    </section>
    <section class="plan-section">
      <h3>The outcome</h3>
      <p style="white-space: pre-wrap">{{ order.objective }}</p>
    </section>
    <section v-if="order.brief" class="plan-section" data-test="brief">
      <h3>Foreman's brief</h3>
      <p style="white-space: pre-wrap">{{ order.brief }}</p>
    </section>
    <section class="plan-section">
      <h3>Acceptance contract</h3>
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
    </section>
    <section v-if="order.spec" class="plan-section" data-test="spec">
      <h3>Architect's spec</h3>
      <p style="white-space: pre-wrap">{{ order.spec }}</p>
    </section>
    <section class="plan-section">
      <h3>How the crew will build it</h3>
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
          <div>
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
    </section>
    <section class="plan-section">
      <h3>Scope boundaries</h3>
      <p>
        Work follows the recorded outcome and acceptance contract. New ambiguity
        returns to the human. Verification evidence identifies its revision.
      </p>
    </section>
    <button
      class="artifact-chip"
      aria-label="Download blueprint"
      @click="emit('export')"
    >
      <Download class="icon" />Download this blueprint as Markdown<span
        class="grow"
      ></span
      ><span class="muted">.md</span>
    </button>
  </div>
</template>
