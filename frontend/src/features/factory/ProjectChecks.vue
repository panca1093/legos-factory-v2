<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from "vue";
import { useChecks, dryRunOf, failsOnMain } from "../../stores/checks";
import { useSession } from "../../stores/session";
import type { CheckStep } from "../../types/api";

const props = defineProps<{ projectId: string }>();
const checks = useChecks(),
  session = useSession();
watch(() => props.projectId, (id) => void checks.load(id), {
  immediate: true,
});
onBeforeUnmount(() => checks.stop());

const approved = computed(() => checks.status?.profile ?? null);
const proposal = computed(() => checks.status?.proposal ?? null);
const run = computed(() => checks.status?.run);
const draft = computed(() => checks.draft);
const services = computed(() => draft.value?.services?.names ?? []);
const env = computed(() => Object.entries(draft.value?.env ?? {}));
const target = (s: CheckStep) => (s.dir ? `${s.dir}/` : "./");
const outcomeLabel = (s: CheckStep, kind: "setup" | "check") => {
  const r = dryRunOf(proposal.value?.results, kind, s);
  if (!r) return "edited · not dry-run";
  return r.outcome === "PASS"
    ? "passes"
    : r.outcome === "FAIL"
      ? `fails (exit ${r.exit_code ?? "?"})`
      : "not verified";
};
const tone = (s: CheckStep, kind: "setup" | "check") => {
  const o = dryRunOf(proposal.value?.results, kind, s)?.outcome;
  return o === "PASS" ? "green" : o === "FAIL" ? "red" : "orange";
};
const tailOf = (s: CheckStep) =>
  dryRunOf(proposal.value?.results, "check", s)?.output_tail ?? "";
</script>

<template>
  <section class="pc" data-test="project-checks" aria-label="Checks">
    <div class="row between wrap">
      <strong>Checks</strong>
      <button
        v-if="session.canWrite"
        class="lg-btn small"
        data-test="suggest-checks"
        :disabled="checks.running || checks.busy"
        @click="checks.suggest(projectId)"
      >
        {{ approved ? "Suggest again" : "Suggest checks" }}
      </button>
    </div>
    <p v-if="checks.status?.stale" class="notice" data-test="checks-stale">
      Checks may be stale · a file they were derived from changed.
      <button
        v-if="session.canWrite"
        class="lg-btn small ghost"
        :disabled="checks.running"
        @click="checks.suggest(projectId)"
      >
        Suggest again
      </button>
    </p>
    <p
      v-if="checks.running"
      class="small muted"
      role="status"
      data-test="checks-running"
    >
      Reading the repository and dry-running the checks on the default
      branch…
    </p>
    <p
      v-else-if="run?.status === 'failed'"
      class="error-state"
      role="alert"
      data-test="checks-failed"
    >
      Suggestion failed: {{ run.error }}
    </p>
    <p v-if="checks.error" class="error-state" role="alert">
      {{ checks.error }}
    </p>

    <template v-if="proposal && draft">
      <div class="small muted">
        Proposed from {{ proposal.base_sha.slice(0, 7) }} · {{ proposal.model }}
      </div>
      <div class="pc-list" data-test="proposal">
        <template v-for="(s, i) in draft.setup ?? []" :key="'s' + i">
          <div v-if="!s.per_run" class="pc-item">
            <div class="row between wrap">
              <span
                ><strong>{{ s.name }}</strong>
                <span class="pill">setup</span></span
              >
              <span class="pill" :class="tone(s, 'setup')">{{
                outcomeLabel(s, "setup")
              }}</span>
            </div>
            <input
              :value="s.command"
              :aria-label="`${s.name} command`"
              class="mono"
              @input="
                checks.setCommand(
                  'setup',
                  i,
                  ($event.target as HTMLInputElement).value,
                )
              "
            />
            <div class="small muted">in {{ target(s) }}</div>
          </div>
        </template>
        <div
          v-for="(s, i) in draft.checks"
          :key="'c' + i"
          class="pc-item"
          :data-test="`check-${s.name}`"
        >
          <div class="row between wrap">
            <span
              ><strong>{{ s.name }}</strong>
              <span v-if="s.optional" class="pill">optional</span></span
            >
            <span class="row wrap">
              <span
                v-if="failsOnMain(proposal.results, s)"
                class="pill red"
                data-test="fails-on-main"
                >fails on main</span
              >
              <span v-else class="pill" :class="tone(s, 'check')">{{
                outcomeLabel(s, "check")
              }}</span>
              <button
                v-if="failsOnMain(proposal.results, s) && !s.optional"
                class="lg-btn small ghost"
                data-test="make-optional"
                @click="checks.makeOptional(i)"
              >
                Make optional
              </button>
            </span>
          </div>
          <input
            :value="s.command"
            :aria-label="`${s.name} command`"
            class="mono"
            @input="
              checks.setCommand(
                'checks',
                i,
                ($event.target as HTMLInputElement).value,
              )
            "
          />
          <div class="small muted">
            in {{ target(s) }}<template v-if="s.source"> · {{ s.source }}</template>
          </div>
          <div v-if="proposal.notes?.[s.name]" class="small muted">
            {{ proposal.notes[s.name] }}
          </div>
          <pre v-if="failsOnMain(proposal.results, s)" class="pc-out">{{
            tailOf(s)
          }}</pre>
        </div>
      </div>
      <div v-if="services.length || env.length" class="small">
        <div v-if="services.length">
          <strong>Services</strong> from {{ draft.services?.compose }}:
          <span class="mono">{{ services.join(", ") }}</span>
        </div>
        <div v-if="env.length">
          <strong>Env the checks receive</strong>
          <div v-for="[k, v] in env" :key="k" class="mono">
            {{ k }}={{ v }}
          </div>
        </div>
      </div>
      <div v-if="session.canWrite" class="row">
        <button
          class="lg-btn primary"
          data-test="approve-checks"
          :disabled="checks.busy || checks.running"
          @click="checks.approve()"
        >
          Approve
        </button>
        <button
          class="lg-btn ghost"
          data-test="discard-checks"
          :disabled="checks.busy || checks.running"
          @click="checks.discard()"
        >
          Discard
        </button>
      </div>
    </template>

    <template v-else-if="approved">
      <div class="pc-list" data-test="approved">
        <div v-for="(s, i) in approved.checks" :key="i" class="pc-item">
          <div class="row between wrap">
            <strong>{{ s.name }}</strong>
            <span v-if="s.optional" class="pill">optional</span>
          </div>
          <div class="mono small">{{ s.command }}</div>
          <div class="small muted">
            in {{ target(s) }}<template v-if="s.source"> · {{ s.source }}</template>
          </div>
        </div>
      </div>
    </template>
    <p
      v-else-if="!checks.running && run?.status !== 'failed'"
      class="small muted"
    >
      No checks for this project yet. Suggest checks reads the repository's CI
      and build files and proposes them; nothing is written to the repository.
    </p>
  </section>
</template>
