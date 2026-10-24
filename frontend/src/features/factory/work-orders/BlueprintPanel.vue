<script setup lang="ts">
import {
  ArrowRight,
  Check,
  MessageSquare,
  Download,
  ShieldCheck,
  CircleCheck,
} from "lucide-vue-next";
import type { WorkOrder, Task } from "../../../types/api";
import BrickBot from "../../../components/agents/BrickBot.vue";
defineProps<{
  order: WorkOrder;
  tasks: Task[];
  canWrite: boolean;
  canClarify: boolean;
  busy: boolean;
}>();
const emit = defineEmits<{
  draft: [];
  approve: [];
  revise: [];
  clarify: [];
  export: [];
}>();
</script>
<template>
  <div>
    <section v-if="order.state === 'CREATED'" class="callout">
      <div class="row">
        <BrickBot persona="foreman" class="compact" /><strong
          >Give the crew a clear starting point.</strong
        >
      </div>
      <p>
        Foreman will draft a blueprint and task graph. Architect reviews the
        scope and acceptance contract before implementation.
      </p>
      <button
        v-if="canWrite"
        class="btn violet"
        style="margin-top: 14px"
        :disabled="busy"
        @click="emit('draft')"
      >
        Draft blueprint<ArrowRight class="icon" />
      </button>
    </section>
    <section
      v-else-if="
        order.state === 'WAITING_FOR_HUMAN' &&
        order.clarif_question &&
        !order.clarif_answer
      "
      class="callout orange"
    >
      <div class="row">
        <BrickBot persona="architect" class="compact" />
        <div>
          <strong>One decision before we build</strong>
          <p>{{ order.clarif_question }}</p>
        </div>
      </div>
      <p>
        Architect won’t guess this contract. Your answer becomes part of the
        recorded blueprint.
      </p>
      <button
        v-if="canClarify"
        class="btn primary"
        style="margin-top: 14px"
        data-test="clarify"
        @click="emit('clarify')"
      >
        <MessageSquare class="icon" />Answer clarification
      </button>
      <p v-else class="help">
        The creator or a workspace admin can record this decision.
      </p>
    </section>
    <section
      v-else-if="order.state === 'ARCHITECT_REVIEW'"
      class="callout green"
    >
      <div class="row">
        <BrickBot persona="architect" class="compact" /><strong
          >Your blueprint is ready for review. Your call.</strong
        >
      </div>
      <p>
        Inspect the outcome, contract and task dependencies before authorizing
        execution.
      </p>
      <div class="row wrap" style="margin-top: 14px">
        <button
          class="btn primary"
          data-test="approve-plan"
          :disabled="busy || !canWrite"
          @click="emit('approve')"
        >
          <Check class="icon" />Approve plan</button
        ><button
          class="btn"
          data-test="revise-plan"
          :disabled="busy || !canWrite"
          @click="emit('revise')"
        >
          <MessageSquare class="icon" />Request revision
        </button>
      </div>
    </section>
    <section v-else-if="tasks.length" class="callout">
      <div class="row between">
        <span class="row" style="gap: 8px"
          ><ShieldCheck class="icon" /><strong class="small"
            >Recorded blueprint · revision {{ order.revision }}</strong
          ></span
        >
      </div>
      <p>
        {{
          order.clarif_answer
            ? "Decision recorded: " + order.clarif_answer
            : "Scope, dependency graph and evidence expectations stay attached to this work order."
        }}
      </p>
    </section>
    <section class="plan-section">
      <h3>The outcome</h3>
      <p style="white-space: pre-wrap">{{ order.objective }}</p>
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
