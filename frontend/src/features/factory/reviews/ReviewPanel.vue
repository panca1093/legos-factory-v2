<script setup lang="ts">
import { computed } from "vue";
import type { QualityCheck, Finding, WOEvent } from "../../../types/api";
import ActorBadge from "../ActorBadge.vue";
import ChecksGrid from "./ChecksGrid.vue";
import { architectReport } from "../runs/agentActivity";
const props = defineProps<{
  checks: QualityCheck[];
  findings: Finding[];
  revision: number;
  events?: WOEvent[];
  state?: string;
  canWrite?: boolean;
  busy?: boolean;
}>();
type FindingAction = "confirm" | "dispute" | "resolve" | "escalate";
const emit = defineEmits<{ finding: [string, FindingAction, string] }>();
const report = computed(() => architectReport(props.events ?? []));
const gapTone: Record<string, string> = {
  missing: "red",
  contradicts: "red",
  partial: "orange",
  unrequested: "orange",
};
const statusTone: Record<string, string> = {
  open: "red",
  confirmed: "red",
  escalated: "orange",
  disputed: "",
  resolved: "green",
};
const canDispose = computed(
  () =>
    props.canWrite &&
    !props.busy &&
    !["READY", "CANCELLED"].includes(props.state ?? ""),
);
function dispose(f: Finding, action: FindingAction) {
  let note = "";
  if (action === "dispute" || action === "escalate") {
    note =
      window
        .prompt(
          action === "dispute"
            ? `Why is "${f.title}" not a real problem?`
            : `What decision does "${f.title}" need? The order pauses until you resume it.`,
        )
        ?.trim() ?? "";
    if (!note) return;
  }
  emit("finding", f.id, action, note);
}
</script>
<template>
  <div class="fx-stack">
    <ChecksGrid :checks="checks" :revision="revision" />
    <div class="two">
      <section class="fx-card" data-test="architect-verdicts">
        <h3>
          <ActorBadge actor="architect" small />Architect gaps
          <span class="fx-pill">{{ report?.gaps.length ?? 0 }}</span>
        </h3>
        <template v-if="report">
          <p class="tiny muted">
            Revision {{ report.revision }}, converge round {{ report.round }}
          </p>
          <article
            v-for="g in report.gaps"
            :key="g.id"
            class="fx-verdict"
            data-test="verdict"
          >
            <div class="fx-row">
              <span class="fx-code">{{ g.id }}</span>
              <span class="fx-pill" :class="gapTone[g.type]">{{ g.type }}</span>
              <span class="fx-pill">{{ g.layer }}</span>
              <span class="tiny muted">{{ g.cites }}</span>
            </div>
            <p v-if="g.fix" class="small" data-test="verdict-fix">
              <strong>Suggested fix:</strong> {{ g.fix }}
            </p>
            <details class="small muted">
              <summary>Evidence</summary>
              {{ g.evidence }}
            </details>
          </article>
          <p v-if="!report.gaps.length" class="small muted">No gaps found.</p>
        </template>
        <p v-else class="fx-empty">
          Runs after the checks pass, in parallel with the Inspector. Only
          available when Jev is on.
        </p>
      </section>
      <section class="fx-card">
        <h3>
          <ActorBadge actor="inspector" small />Inspector findings
          <span class="fx-pill">{{ findings.length }}</span>
        </h3>
        <article
          v-for="f in findings"
          :key="f.id"
          class="fx-finding"
          :data-finding="f.status"
        >
          <div class="fx-row">
            <span
              class="fx-pill"
              :class="f.severity === 'blocker' ? 'red' : 'orange'"
              >{{ f.severity }}</span><span
              v-if="f.specialty"
              class="fx-pill"
              >{{ f.specialty.replace("_", "/") }}</span
            ><strong class="small grow">{{ f.title }}</strong
            ><span class="fx-pill" :class="statusTone[f.status]">{{
              f.status
            }}</span>
          </div>
          <p class="small muted">{{ f.detail }}</p>
          <p v-if="f.location" class="tiny mono muted">{{ f.location }}</p>
          <p v-if="f.note" class="small">
            <strong>Human note:</strong> {{ f.note }}
          </p>
          <div v-if="canDispose && f.status !== 'resolved'" class="fx-actions">
            <button
              v-if="f.status !== 'confirmed'"
              class="lg-btn small"
              @click="dispose(f, 'confirm')"
            >
              Confirm</button
            ><button
              v-if="f.status !== 'disputed'"
              class="lg-btn small"
              @click="dispose(f, 'dispute')"
            >
              Dispute</button
            ><button class="lg-btn small" @click="dispose(f, 'resolve')">
              Resolve</button
            ><button
              v-if="f.status !== 'escalated'"
              class="lg-btn small"
              @click="dispose(f, 'escalate')"
            >
              Escalate
            </button>
          </div>
        </article>
        <p v-if="!findings.length" class="fx-empty">No findings recorded.</p>
      </section>
    </div>
  </div>
</template>
<style scoped>
.two {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 24px;
  align-items: start;
}
.two > section {
  display: grid;
  gap: 12px;
}
.two > section :is(h3, p) {
  margin: 0;
}
@media (max-width: 720px) {
  .two {
    grid-template-columns: 1fr;
  }
}
</style>
