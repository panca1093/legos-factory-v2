<script setup lang="ts">
import { computed } from "vue";
import { Check, ExternalLink, MessageSquare, X } from "lucide-vue-next";
import type { Run, WODiff } from "../../../types/api";
import type { shipState } from "../../../composables/presentation";
import DiffView from "./DiffView.vue";
import ReasonText from "../ReasonText.vue";
import { diffFromApi, diffFromRuns } from "./diff";
const props = defineProps<{
  runs: Run[];
  state: string;
  canWrite: boolean;
  busy: boolean;
  ship?: ReturnType<typeof shipState>;
  reason?: string;
  issues?: string[];
  publishNote?: string;
  diff?: WODiff | null;
  refreshFailed?: boolean;
  orderId?: string;
  prUrl?: string;
  prRef?: string;
  sourceRef?: string;
}>();
const emit = defineEmits<{
  decision: ["approve" | "request_changes" | "decline"];
}>();
const builders = computed(() => props.runs.filter((r) => r.kind === "builder")),
  available = computed(() => !!props.diff?.available),
  files = computed(() =>
    available.value ? diffFromApi(props.diff!) : diffFromRuns(props.runs),
  );
const canDecide = computed(
  () => props.canWrite && props.state === "NEEDS_HUMAN_REVIEW" && !props.busy,
);
const shipNote: Record<string, string> = {
  open: "The pull request is open: review and merge it on the provider. Nothing is merged until its review and branch protection pass; the factory keeps tracking the merge.",
  open_failed:
    "The pull request could not be opened. The reason is in Activity.",
  closed:
    "The PR was closed without merging. Polling continues: if it is reopened and merged, this order still ships.",
  tracking_stopped:
    "The factory can no longer read this pull request, so its merge status will not update. The reason is in Activity.",
  merged: "The pull request was merged.",
  local: "The decision was recorded locally. No pull request was opened.",
};
</script>
<template>
  <div class="fx-stack">
    <section class="fx-card fx-pr" data-test="ship-state">
      <div class="fx-pr-head">
        <span v-if="ship" class="fx-pill" :class="ship.tone">{{
          ship.label
        }}</span
        ><span v-else class="fx-pill">Not at Ship yet</span>
        <a
          v-if="prUrl"
          class="fx-link"
          data-test="pr-link"
          :href="prUrl"
          target="_blank"
          rel="noopener noreferrer"
          >{{ prRef || prUrl }} <ExternalLink class="icon"
        /></a>
        <span class="grow"></span>
        <div v-if="state === 'NEEDS_HUMAN_REVIEW'" class="fx-actions">
          <button
            class="lg-btn primary small"
            data-test="pr-approve"
            :disabled="!canDecide"
            @click="emit('decision', 'approve')"
          >
            <Check class="icon" />Approve</button
          ><button
            class="lg-btn small"
            data-test="pr-changes"
            :disabled="!canDecide"
            @click="emit('decision', 'request_changes')"
          >
            <MessageSquare class="icon" />Request changes</button
          ><button
            class="lg-btn small"
            data-test="pr-decline"
            :disabled="!canDecide"
            @click="emit('decision', 'decline')"
          >
            <X class="icon" />Decline
          </button>
        </div>
      </div>
      <ReasonText
        v-if="reason && ship?.key === 'approval'"
        label="Before you approve:"
        :text="reason"
        :items="issues"
      />
      <p v-if="ship?.key === 'approval'" class="small muted">
        {{ publishNote }}
      </p>
      <p v-else-if="ship" class="small muted">{{ shipNote[ship.key] }}</p>
      <p v-if="sourceRef" class="tiny muted">
        Source issue: {{ sourceRef.replace("jira:", "") }}
      </p>
    </section>
    <p v-if="refreshFailed" class="tiny muted" data-test="diff-stale">
      Couldn't refresh the diff; showing the last loaded version.
    </p>
    <DiffView
      v-if="files.length"
      :files="files"
      :reset-key="orderId"
      :meta="
        available
          ? {
              base: diff?.base,
              head: diff?.head,
              revision: diff?.revision,
              truncated: diff?.truncated,
            }
          : undefined
      "
    />
    <p v-else-if="available" class="fx-card small muted" data-test="diff-empty">
      This work order has no file changes against {{ diff?.base?.slice(0, 7) }}.
    </p>
    <p
      v-else-if="diff?.reason"
      class="fx-card small muted"
      data-test="diff-unavailable"
    >
      {{ diff.reason }}
    </p>
    <details v-if="!available" class="fx-card" :open="!files.length">
      <summary class="fx-sum"><h3>Builder output</h3></summary>
      <p class="help">
        Recorded harness output. A repository diff is not available for this
        work order, so file-level changes appear only when a run printed a
        unified diff.
      </p>
      <details
        v-for="r in builders"
        :key="r.id"
        class="fx-run"
        :open="builders.length === 1"
      >
        <summary>
          <span class="fx-pill">{{ r.harness }}</span
          ><strong class="small">{{ r.task_id || "Remediation run" }}</strong
          ><span class="fx-pill">{{ r.state }}</span>
        </summary>
        <pre class="output">{{
          r.error || r.output || "No output recorded."
        }}</pre>
      </details>
      <p v-if="!builders.length" class="fx-empty">
        No implementation output recorded yet.
      </p>
    </details>
  </div>
</template>
