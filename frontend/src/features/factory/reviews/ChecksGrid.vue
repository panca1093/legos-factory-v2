<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { QualityCheck } from "../../../types/api";
import ActorBadge from "../ActorBadge.vue";
import { failsOnMain, realChecks } from "./checks";
const props = defineProps<{ checks: QualityCheck[]; revision: number }>();
const picked = ref<string | null>(null);
const real = computed(() => realChecks(props.checks));
const current = computed(() =>
  real.value.filter((c) => c.revision === props.revision),
);
const earlier = computed(() =>
  [...new Set(real.value.map((c) => c.revision))]
    .filter((r) => r !== props.revision)
    .sort((a, b) => b - a)
    .map((revision) => ({
      revision,
      checks: real.value.filter((c) => c.revision === revision),
    })),
);
const onMain = (c: QualityCheck) => failsOnMain(props.checks, c);
watch(
  () => props.revision,
  () => (picked.value = null),
);
const pickedCheck = computed(
  () =>
    current.value.find((c) => c.id === picked.value) ??
    earlier.value.flatMap((g) => g.checks).find((c) => c.id === picked.value) ??
    null,
);
const selected = computed(
  () =>
    pickedCheck.value ??
    current.value.find((c) => c.outcome === "FAIL") ??
    null,
);
const selectedHistoric = computed(
  () => !!selected.value && selected.value.revision !== props.revision,
);
const failed = computed(
  () => current.value.filter((c) => c.outcome === "FAIL").length,
);
const failedOnMain = computed(
  () => current.value.filter((c) => c.outcome === "FAIL" && onMain(c)).length,
);
const tone = (c: QualityCheck) =>
  c.outcome === "PASS" ? "green" : c.outcome === "FAIL" ? "red" : "orange";
</script>
<template>
  <section class="fx-card checks" data-test="checks-card">
    <h3>
      <ActorBadge actor="factory" small />Checks
      <span class="fx-pill" :class="failed ? 'red' : ''">
        {{
          current.length -
          failed -
          current.filter((c) => c.outcome === "UNVERIFIED").length
        }}
        passed · {{ failed }} failed
      </span>
      <span
        v-if="failedOnMain"
        class="fx-pill orange"
        data-test="main-fail-pill"
        >{{ failedOnMain }} failed on main too</span
      >
    </h3>
    <p class="tiny muted">
      <span>Current revision {{ revision }}</span
      >. Historical passes do not verify it.
    </p>
    <div v-if="current.length" class="cgrid">
      <button
        v-for="c in current"
        :key="c.id"
        type="button"
        class="ck"
        :class="{
          fail: c.outcome === 'FAIL',
          base: onMain(c),
          sel: selected?.id === c.id,
        }"
        :data-check="c.id"
        :aria-pressed="selected?.id === c.id"
        @click="picked = c.id"
      >
        <span class="ckt">
          <strong>{{ c.name }}</strong>
          <span class="fx-pill" :class="tone(c)">{{ c.outcome }}</span>
        </span>
        <code>{{ c.command }}</code>
        <span v-if="onMain(c)" class="note" data-test="main-fail-tag"
          >Fails on main too</span
        >
      </button>
    </div>
    <div
      v-if="selected && !selectedHistoric"
      class="detail"
      data-test="check-detail"
    >
      <div class="fx-row">
        <span class="fx-pill" :class="tone(selected)">{{
          selected.outcome
        }}</span>
        <strong class="small">{{ selected.name }}</strong>
        <span class="small muted"
          >Revision {{ selected.revision }} · Exit code:
          {{ selected.exit_code }}</span
        >
      </div>
      <code class="mono">{{ selected.command }}</code>
      <pre class="output">{{ selected.output || "No output recorded." }}</pre>
      <p class="tiny muted">
        {{ selected.created_at }} · Check {{ selected.id }}
      </p>
    </div>
    <details v-for="g in earlier" :key="g.revision" class="earlier">
      <summary class="fx-sum">
        <span>Historical evidence · revision {{ g.revision }}</span>
        <span class="tiny muted">{{ g.checks.length }} checks</span>
      </summary>
      <button
        v-for="c in g.checks"
        :key="c.id"
        type="button"
        class="row-btn"
        :data-check="c.id"
        :aria-pressed="selected?.id === c.id"
        @click="picked = c.id"
      >
        <span class="fx-pill" :class="tone(c)">{{ c.outcome }}</span>
        <strong class="small">{{ c.name }}</strong>
        <code class="grow">{{ c.command }}</code>
      </button>
      <div
        v-if="selected && selectedHistoric && selected.revision === g.revision"
        class="detail"
        data-test="check-detail"
      >
        <div class="fx-row">
          <span class="fx-pill" :class="tone(selected)">{{
            selected.outcome
          }}</span>
          <strong class="small">{{ selected.name }}</strong>
          <span class="small muted"
            >Revision {{ selected.revision }} · Exit code:
            {{ selected.exit_code }}</span
          >
        </div>
        <code class="mono">{{ selected.command }}</code>
        <pre class="output">{{ selected.output || "No output recorded." }}</pre>
        <p class="tiny muted">
          {{ selected.created_at }} · Check {{ selected.id }}
        </p>
      </div>
    </details>
    <p v-if="!real.length" class="fx-empty">No verification evidence yet.</p>
  </section>
</template>
<style scoped>
.checks {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.checks h3,
.checks p {
  margin: 0;
}
.cgrid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
}
.ck {
  display: grid;
  gap: 6px;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: 12px;
  background: var(--surface);
  color: var(--ink);
  text-align: left;
  cursor: pointer;
}
.ck .ckt {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
.ck strong {
  font-size: 13.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ck code {
  display: block;
  overflow: hidden;
  color: var(--muted);
  font-family: var(--font-mono);
  font-size: 11.5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ck.fail {
  border-color: color-mix(in srgb, var(--danger) 45%, var(--line));
}
.ck.base {
  border-style: dashed;
}
.ck .note {
  color: var(--muted);
  font-size: 12px;
}
.ck.sel {
  box-shadow: 0 0 0 2px var(--brand);
}
.ck:focus-visible,
.row-btn:focus-visible {
  outline: 2px solid var(--brand);
  outline-offset: 2px;
}
.detail {
  display: grid;
  gap: 8px;
  min-width: 0;
}
.detail .mono {
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
}
.detail .output {
  margin: 0;
  padding: 12px 14px;
  overflow-x: auto;
  border-radius: 12px;
  background: var(--surface-2);
  font: 12px/1.6 var(--font-mono);
  max-height: 320px;
}
.row-btn {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 6px 4px;
  border: 0;
  background: none;
  color: var(--ink);
  text-align: left;
  cursor: pointer;
}
.row-btn code {
  overflow: hidden;
  color: var(--muted);
  font-size: 11.5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@media (max-width: 1080px) {
  .cgrid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 720px) {
  .cgrid {
    grid-template-columns: 1fr;
  }
}
</style>
