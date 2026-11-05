<script setup lang="ts">
import { computed } from "vue";
import { Ban, ExternalLink, MessageSquare, Pause, Play } from "lucide-vue-next";
import type { Run, Task, WOEvent, WorkOrder } from "../../../types/api";
import {
  actorOf,
  isFinished,
  orderStatus,
  shipState,
  taskState,
} from "../../../composables/presentation";
import ActorBadge from "../ActorBadge.vue";
import { escalationReason, lastMessage } from "../runs/agentActivity";
const props = defineProps<{
  order: WorkOrder;
  tasks: Task[];
  runs: Run[];
  events: WOEvent[];
  canWrite: boolean;
  canClarify: boolean;
  busy: boolean;
}>();
const emit = defineEmits<{
  draft: [];
  approvePlan: [];
  revisePlan: [];
  clarify: [];
  pause: [];
  resume: [];
  cancel: [];
  goto: [string];
}>();
const paused = computed(() => !!props.order.paused_at),
  finished = computed(() => isFinished(props.order)),
  status = computed(() => orderStatus(props.order)),
  ship = computed(() => shipState(props.order)),
  reason = computed(() => escalationReason(props.events)),
  last = computed(() => props.events.at(-1)),
  done = computed(
    () =>
      props.tasks.filter((t) => taskState(t.id, props.runs) === "completed")
        .length,
  );
const asking = computed(
  () =>
    props.order.state === "WAITING_FOR_HUMAN" &&
    !!props.order.clarif_question &&
    !props.order.clarif_answer,
);
const planEscalated = computed(
  () =>
    props.order.state === "ARCHITECT_REVIEW" &&
    props.order.plan_review === "approved",
);
const headline = computed(() => {
  const o = props.order;
  if (o.pr_merged_at) return "Merged: this work order is done";
  if (o.state === "CANCELLED") return "Cancelled";
  if (o.state === "BLOCKED") return "Blocked";
  if (asking.value) return "The Architect needs your input";
  if (planEscalated.value) return "The plan needs your decision";
  const t: Record<string, string> = {
    CREATED: "Ready to draft the blueprint",
    REQUIREMENT_ANALYSIS: "The Foreman is shaping the brief",
    TASK_DECOMPOSITION: "The Architect is drafting the spec and plan",
    SPEC_AND_PLAN_GENERATION: "The Architect is drafting the spec and plan",
    PLANNING: "The Architect is drafting the spec and plan",
    ARCHITECT_REVIEW: "The Foreman is reviewing the Architect's plan",
    WAITING_FOR_HUMAN: "The Inspector escalated: your decision is needed",
    QUEUED: "Build is queued",
    IMPLEMENTING: "The Builder is building",
    VERIFYING: "Running checks",
    SPECIALIST_REVIEW: "The Inspector and Architect are reviewing",
    PR_PREPARATION: "Preparing the pull request",
    MERGE_READINESS: "Checking merge readiness",
  };
  return t[o.state] ?? status.value.label;
});
</script>
<template>
  <section
    class="fx-now"
    :class="{ wait: actorOf(order) === 'you' && !finished }"
    data-test="now-card"
  >
    <div class="fx-now-head">
      <ActorBadge :actor="actorOf(order)" />
      <div class="grow">
        <div class="fx-kind">Now</div>
        <h2
          role="status"
          :data-test="
            order.state === 'ARCHITECT_REVIEW' && !planEscalated
              ? 'foreman-reviewing'
              : undefined
          "
        >
          {{ headline }}
          <span v-if="paused" class="fx-pill orange">Paused</span>
        </h2>
      </div>
      <div
        v-if="
          !finished &&
          !['READY', 'BLOCKED', 'NEEDS_HUMAN_REVIEW'].includes(order.state)
        "
        class="fx-actions"
      >
        <button
          v-if="!paused"
          class="lg-btn small"
          data-test="execution-pause"
          :disabled="!canWrite || busy"
          @click="emit('pause')"
        >
          <Pause class="icon" />Pause</button
        ><button
          v-else
          class="lg-btn small primary"
          data-test="execution-step"
          :disabled="!canWrite || busy"
          @click="emit('resume')"
        >
          <Play class="icon" />Resume</button
        ><button
          v-if="canWrite"
          class="lg-btn small ghost danger"
          data-test="cancel-order"
          :disabled="busy"
          @click="emit('cancel')"
        >
          <Ban class="icon" />Cancel
        </button>
      </div>
    </div>
    <div class="fx-now-body">
      <p v-if="paused" class="fx-reason" data-test="pause-reason">
        {{ order.pause_reason || "Paused." }} Queued work waits; nothing is
        lost.
      </p>
      <template v-if="order.state === 'CREATED'">
        <p class="small muted">
          The Foreman drafts the brief, then the Architect plans from it.
        </p>
        <button
          v-if="canWrite"
          class="lg-btn primary small"
          data-test="draft-blueprint"
          :disabled="busy"
          @click="emit('draft')"
        >
          Draft blueprint
        </button>
      </template>
      <template v-else-if="asking">
        <p class="small">{{ order.clarif_question }}</p>
        <button
          v-if="canClarify"
          class="lg-btn primary small"
          data-test="clarify"
          @click="emit('clarify')"
        >
          <MessageSquare class="icon" />Answer clarification
        </button>
        <p v-else class="small muted">
          The creator or a workspace admin can answer this.
        </p>
      </template>
      <template v-else-if="order.state === 'WAITING_FOR_HUMAN'">
        <p class="fx-reason">
          {{
            lastMessage(events, "escalated", "clarification_escalated") ||
            "Review found something it could not settle."
          }}
        </p>
        <button class="lg-btn primary small" @click="emit('goto', 'review')">
          Open Review
        </button>
      </template>
      <template v-else-if="order.state === 'BLOCKED'">
        <p class="fx-reason" data-test="blocked-reason">
          {{
            lastMessage(events, "readiness_blocked") ||
            "The factory blocked this order."
          }}
        </p>
        <button class="lg-btn small" @click="emit('goto', 'review')">
          Open Review
        </button>
      </template>
      <template v-else-if="planEscalated">
        <p class="fx-reason">
          <strong>Why you are asked:</strong>
          {{ reason || "The plan gate escalated this plan." }}
          <template v-if="order.plan_revisions"
            >It was revised {{ order.plan_revisions }} of 2 times.</template
          >
        </p>
        <div class="fx-actions">
          <button
            class="lg-btn primary small"
            data-test="approve-plan"
            :disabled="busy || !canWrite"
            @click="emit('approvePlan')"
          >
            Approve plan</button
          ><button
            class="lg-btn small"
            data-test="revise-plan"
            :disabled="busy || !canWrite"
            @click="emit('revisePlan')"
          >
            Request revision
          </button>
        </div>
      </template>
      <template v-else-if="order.state === 'NEEDS_HUMAN_REVIEW'">
        <p class="fx-reason">
          <strong>{{ ship?.label }}.</strong>
          {{
            reason || "The checks passed and the done gate handed this to you."
          }}
        </p>
        <button class="lg-btn primary small" @click="emit('goto', 'changes')">
          Review changes &amp; PR
        </button>
      </template>
      <template v-else-if="order.state === 'READY' && ship">
        <p class="small">
          <span class="fx-pill" :class="ship.tone">{{ ship.label }}</span>
          <a
            v-if="order.pr_url"
            class="fx-link"
            :href="order.pr_url"
            target="_blank"
            rel="noopener noreferrer"
            >{{ order.pr_ref || order.pr_url }} <ExternalLink class="icon"
          /></a>
        </p>
        <p v-if="order.pr_closed_at && !order.pr_merged_at" class="small muted">
          Closed without merging. Reopen it, or request a fix.
        </p>
        <p v-else-if="order.pr_track_failed_at" class="small muted">
          The factory can no longer read this pull request; check it on the
          provider.
        </p>
        <button class="lg-btn small" @click="emit('goto', 'changes')">
          Changes &amp; PR
        </button>
      </template>
      <p
        v-else-if="order.state === 'IMPLEMENTING' || order.state === 'QUEUED'"
        class="small muted"
      >
        {{ done }} of {{ tasks.length }} tasks done.
      </p>
      <p
        v-if="
          last &&
          !finished &&
          !planEscalated &&
          !asking &&
          order.state !== 'BLOCKED'
        "
        class="tiny muted fx-last"
      >
        Latest: {{ last.message }}
      </p>
    </div>
  </section>
</template>
