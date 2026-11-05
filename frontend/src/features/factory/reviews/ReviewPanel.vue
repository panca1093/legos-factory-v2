<script setup lang="ts">
import { computed, ref } from "vue";
import { ChevronRight } from "lucide-vue-next";
import type { QualityCheck, Finding, WOEvent } from "../../../types/api";
import AppDialog from "../../../components/ui/AppDialog.vue";
import ActorBadge from "../ActorBadge.vue";
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
const selected = ref<QualityCheck | null>(null);
const groups = computed(() =>
  [...new Set(props.checks.map((c) => c.revision))]
    .sort((a, b) => b - a)
    .map((revision) => ({
      revision,
      checks: props.checks.filter((c) => c.revision === revision),
    })),
);
const current = computed(() =>
  props.checks.filter((c) => c.revision === props.revision),
);
const report = computed(() => architectReport(props.events ?? [])),
  failing = computed(() => current.value.some((c) => c.outcome === "FAIL"));
const verdictTone: Record<string, string> = {
  met: "green",
  not_met: "red",
  unclear: "orange",
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
    <section class="fx-card" data-test="architect-verdicts">
      <h3><ActorBadge actor="architect" small />Architect verdicts</h3>
      <template v-if="report">
        <p class="tiny muted">Revision {{ report.revision }}</p>
        <div v-for="v in report.verdicts" :key="v.criterion_id" class="fx-row">
          <span class="fx-code">{{ v.criterion_id }}</span>
          <span class="small grow">{{ v.evidence }}</span>
          <span class="fx-pill" :class="verdictTone[v.status]">{{
            v.status.replace("_", " ")
          }}</span>
        </div>
        <p class="small muted">
          Deviations:
          {{ report.deviations.length ? report.deviations.join("; ") : "none" }}
        </p>
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
            >{{ f.severity }}</span
          ><span v-if="f.specialty" class="fx-pill">{{
            f.specialty.replace("_", "/")
          }}</span
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
    <section class="fx-card">
      <h3>
        <ActorBadge actor="factory" small />Checks
        <span class="fx-pill" :class="failing ? 'red' : ''">
          {{ current.filter((c) => c.outcome === "PASS").length }} passed ·
          {{ current.filter((c) => c.outcome === "FAIL").length }} failed
        </span>
      </h3>
      <p class="tiny muted">
        Current revision {{ revision }}. Historical passes do not verify it.
      </p>
      <details
        v-for="g in groups"
        :key="g.revision"
        :open="g.revision === revision"
      >
        <summary class="fx-sum">
          {{
            g.revision === revision
              ? "Current revision " + g.revision
              : "Historical evidence · revision " + g.revision
          }}
          <span class="tiny muted">{{ g.checks.length }} checks</span>
        </summary>
        <button
          v-for="c in g.checks"
          :key="c.id"
          class="fx-check"
          :data-check="c.id"
          @click="selected = c"
        >
          <span
            class="fx-pill"
            :class="
              c.outcome === 'PASS'
                ? 'green'
                : c.outcome === 'FAIL'
                  ? 'red'
                  : 'orange'
            "
            >{{ c.outcome }}</span
          >
          <strong class="small">{{ c.name }}</strong>
          <code class="fx-code grow">{{ c.command }}</code>
          <ChevronRight class="icon" />
        </button>
      </details>
      <p v-if="!checks.length" class="fx-empty">
        No verification evidence yet.
      </p>
    </section>
  </div>
  <AppDialog
    :open="!!selected"
    :title="selected?.name ?? 'Evidence'"
    @close="selected = null"
    ><div v-if="selected" class="stack">
      <div class="row">
        <span
          class="pill"
          :class="selected.outcome === 'PASS' ? 'green' : 'red'"
          >{{ selected.outcome }}</span
        ><span class="small"
          >Revision {{ selected.revision }} · Exit code:
          {{ selected.exit_code }}</span
        >
      </div>
      <code class="mono">{{ selected.command }}</code>
      <pre class="output">{{ selected.output || "No output recorded." }}</pre>
      <p class="help">{{ selected.created_at }} · Check {{ selected.id }}</p>
    </div></AppDialog
  >
</template>
