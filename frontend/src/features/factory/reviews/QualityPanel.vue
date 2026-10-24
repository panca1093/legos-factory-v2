<script setup lang="ts">
import { computed, ref } from "vue";
import { ShieldCheck, ChevronRight } from "lucide-vue-next";
import type { QualityCheck, Finding } from "../../../types/api";
import AppDialog from "../../../components/ui/AppDialog.vue";
const props = defineProps<{
  checks: QualityCheck[];
  findings: Finding[];
  revision: number;
}>();
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
</script>
<template>
  <div>
    <div class="section-head">
      <div>
        <h2>Confidence comes with evidence</h2>
        <p class="help" style="margin-top: 5px">
          Checks and human judgment each have a job.
        </p>
      </div>
      <ShieldCheck class="icon" />
    </div>
    <div class="notice" style="margin-bottom: 20px">
      Current revision {{ revision }}:
      {{ current.filter((c) => c.outcome === "PASS").length }} passed,
      {{ current.filter((c) => c.outcome === "FAIL").length }} failed.
      Historical passes do not verify this revision.
    </div>
    <section
      v-for="g in groups"
      :key="g.revision"
      class="evidence"
      :class="{ 'evidence-old': g.revision !== revision }"
      style="margin-bottom: 20px"
    >
      <div class="row between">
        <h3>
          {{
            g.revision === revision
              ? "Current revision " + g.revision
              : "Historical evidence · revision " + g.revision
          }}
        </h3>
        <span class="tiny muted">{{ g.checks.length }} checks</span>
      </div>
      <button
        v-for="c in g.checks"
        :key="c.id"
        class="qa-row"
        :class="{ fail: c.outcome === 'FAIL' }"
        :data-check="c.id"
        style="width: 100%; text-align: left"
        @click="selected = c"
      >
        <span
          class="pill"
          :class="
            c.outcome === 'PASS'
              ? 'green'
              : c.outcome === 'FAIL'
                ? 'red'
                : 'orange'
          "
          >{{ c.outcome }}</span
        >
        <div class="grow">
          <strong class="small">{{ c.name }}</strong>
          <p class="help mono">{{ c.command }}</p>
        </div>
        <ChevronRight class="icon" />
      </button>
    </section>
    <div v-if="!checks.length" class="empty">No verification evidence yet.</div>
    <section class="section-gap">
      <div class="section-head">
        <h2>Inspector findings</h2>
        <span class="pill">{{ findings.length }}</span>
      </div>
      <article
        v-for="f in findings"
        :key="f.id"
        class="finding"
        style="background: var(--panel)"
      >
        <div class="row wrap">
          <span
            class="pill"
            :class="f.severity === 'blocker' ? 'red' : 'orange'"
            >{{ f.severity }}</span
          ><strong class="small grow">{{ f.title }}</strong
          ><span class="pill" :class="f.status === 'resolved' ? 'green' : ''">{{
            f.status
          }}</span>
        </div>
        <p class="small muted">{{ f.detail }}</p>
        <p v-if="f.location" class="tiny mono muted">{{ f.location }}</p>
      </article>
      <div v-if="!findings.length" class="empty">No findings recorded.</div>
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
