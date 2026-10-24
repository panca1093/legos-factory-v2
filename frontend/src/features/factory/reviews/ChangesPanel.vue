<script setup lang="ts">
import { computed } from "vue";
import {
  GitPullRequest,
  Check,
  ExternalLink,
  MessageSquare,
  X,
} from "lucide-vue-next";
import type { Run, Finding } from "../../../types/api";
const props = defineProps<{
  runs: Run[];
  findings: Finding[];
  state: string;
  canWrite: boolean;
  busy: boolean;
  publishTo?: string;
  prUrl?: string;
  prRef?: string;
  sourceRef?: string;
}>();
const emit = defineEmits<{
  decision: ["approve" | "request_changes" | "decline"];
}>();
const builders = computed(() => props.runs.filter((r) => r.kind === "builder"));
const canDecide = computed(
  () => props.canWrite && props.state === "NEEDS_HUMAN_REVIEW" && !props.busy,
);
</script>
<template>
  <div class="stack">
    <section class="callout">
      <div class="row">
        <GitPullRequest class="icon" />
        <div class="grow">
          <h3>Your decision closes the loop.</h3>
          <p class="help">
            Review the current evidence, changes and unresolved findings before
            recording a decision.
          </p>
        </div>
      </div>
    </section>
    <section class="panel panel-pad">
      <h2>Builder output</h2>
      <p class="help">
        Recorded harness output. An authoritative repository diff is not exposed
        by the backend.
      </p>
      <details
        v-for="r in builders"
        :key="r.id"
        class="section-gap"
        :open="builders.length === 1"
      >
        <summary class="row wrap" style="cursor: pointer">
          <span class="pill">{{ r.harness }}</span
          ><strong class="small">{{ r.task_id || "Remediation run" }}</strong
          ><span class="pill">{{ r.state }}</span>
        </summary>
        <pre class="output">{{
          r.error || r.output || "No output recorded."
        }}</pre>
      </details>
      <div v-if="!builders.length" class="empty">
        No implementation output recorded yet.
      </div>
    </section>
    <section class="panel panel-pad">
      <h2>Review disposition</h2>
      <article v-for="f in findings" :key="f.id" class="finding">
        <div class="row wrap">
          <span
            class="pill"
            :class="f.severity === 'blocker' ? 'red' : 'orange'"
            >{{ f.severity }}</span
          ><strong class="small grow">{{ f.title }}</strong
          ><span class="pill">{{ f.status }}</span>
        </div>
        <p class="help">{{ f.detail }}</p>
      </article>
      <div v-if="!findings.length" class="notice section-gap">
        No findings recorded. Inspect Quality for the current revision's
        verification evidence.
      </div>
      <div class="form-actions">
        <button
          class="btn primary"
          data-test="pr-approve"
          :disabled="!canDecide"
          @click="emit('decision', 'approve')"
        >
          <Check class="icon" />Approve</button
        ><button
          class="btn"
          data-test="pr-changes"
          :disabled="!canDecide"
          @click="emit('decision', 'request_changes')"
        >
          <MessageSquare class="icon" />Request changes</button
        ><button
          class="btn"
          data-test="pr-decline"
          :disabled="
            !canWrite ||
            busy ||
            !['NEEDS_HUMAN_REVIEW', 'BLOCKED'].includes(state)
          "
          @click="emit('decision', 'decline')"
        >
          <X class="icon" />Decline
        </button>
      </div>
      <p v-if="sourceRef" class="help section-gap">
        Source issue: {{ sourceRef.replace("jira:", "") }}
      </p>
      <div v-if="prUrl" class="notice section-gap" data-test="pr-link">
        Pull request opened:
        <a :href="prUrl" target="_blank" rel="noopener noreferrer"
          >{{ prRef || prUrl }} <ExternalLink class="icon"
        /></a>
        Nothing was merged; the provider's review and branch protection apply.
      </div>
      <div v-else-if="publishTo" class="notice section-gap">
        Approving pushes the verified commit to a work branch and opens a pull
        request on {{ publishTo }}. Nothing is merged.
      </div>
      <div v-else class="notice section-gap">
        This records a local disposition. It does not create, publish or merge a
        pull request.
      </div>
    </section>
  </div>
</template>
