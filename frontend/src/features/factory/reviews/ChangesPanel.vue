<script setup lang="ts">
import { computed } from "vue";
import { Check, ExternalLink, MessageSquare, X } from "lucide-vue-next";
import type { Run } from "../../../types/api";
import type { shipState } from "../../../composables/presentation";
import DiffView from "./DiffView.vue";
import { diffFromRuns } from "./diff";
const props = defineProps<{
  runs: Run[];
  state: string;
  canWrite: boolean;
  busy: boolean;
  ship?: ReturnType<typeof shipState>;
  reason?: string;
  publishTo?: string;
  prUrl?: string;
  prRef?: string;
  sourceRef?: string;
}>();
const emit = defineEmits<{
  decision: ["approve" | "request_changes" | "decline"];
}>();
const builders = computed(() => props.runs.filter((r) => r.kind === "builder")),
  files = computed(() => diffFromRuns(props.runs));
const canDecide = computed(
  () => props.canWrite && props.state === "NEEDS_HUMAN_REVIEW" && !props.busy,
);
const shipNote: Record<string, string> = {
  open: "Nothing is merged until the provider's review and branch protection pass. This order finishes when the PR is merged.",
  closed:
    "The PR was closed without merging. Polling continues: if it is reopened and merged, this order still ships.",
  tracking_stopped:
    "The factory can no longer read this pull request, so its merge status will not update. The reason is in Activity.",
  merged: "The pull request was merged. This work order is done.",
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
      <p v-if="reason && ship?.key === 'approval'" class="fx-reason">
        <strong>Why you are asked:</strong> {{ reason }}
      </p>
      <p v-if="ship?.key === 'approval' && publishTo" class="small muted">
        Approving pushes the verified commit to a work branch and opens a pull
        request on {{ publishTo }}.
      </p>
      <p v-else-if="ship?.key === 'approval'" class="small muted">
        This records a local decision. No pull request is created for this
        project.
      </p>
      <p v-else-if="ship" class="small muted">{{ shipNote[ship.key] }}</p>
      <p v-if="sourceRef" class="tiny muted">
        Source issue: {{ sourceRef.replace("jira:", "") }}
      </p>
    </section>
    <DiffView v-if="files.length" :files="files" />
    <details class="fx-card" :open="!files.length">
      <summary class="fx-sum"><h3>Builder output</h3></summary>
      <p class="help">
        Recorded harness output. The backend does not expose a repository diff,
        so file-level changes appear only when a run printed a unified diff.
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
