<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from "vue";
import {
  useChecks,
  dryRunOf,
  failsOnMain,
  mapToText,
} from "../../stores/checks";
import { useSession } from "../../stores/session";
import type { CheckStep } from "../../types/api";

const props = defineProps<{ projectId: string }>();
const checks = useChecks(),
  session = useSession();
watch(
  () => props.projectId,
  (id) => void checks.load(id),
  {
    immediate: true,
  },
);
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
const leaseError = computed(
  () =>
    proposal.value?.results.find(
      (r) =>
        r.outcome === "UNVERIFIED" &&
        r.output_tail.includes("check services unavailable"),
    )?.output_tail ?? "",
);
const firstPerRun = computed(() =>
  (draft.value?.setup ?? []).findIndex((s) => s.per_run),
);
const hasServices = computed(
  () => !!(draft.value ?? approved.value)?.services?.names?.length,
);
const smoke = computed(() => draft.value?.smoke ?? null);
const warnings = computed(() => {
  const steps = new Set([
    ...(draft.value?.setup ?? []).map((s) => s.name),
    ...(draft.value?.checks ?? []).map((s) => s.name),
  ]);
  return Object.entries(proposal.value?.notes ?? {}).filter(
    ([k]) => !steps.has(k),
  );
});
const val = (e: Event) => (e.target as HTMLInputElement).value;
const tailOf = (s: CheckStep) =>
  dryRunOf(proposal.value?.results, "check", s)?.output_tail ?? "";
</script>

<template>
  <section class="pk" data-test="project-checks" aria-label="Checks">
    <div class="pk-head">
      <h4>Project checks</h4>
      <span v-if="session.canWrite" class="pk-actions">
        <button
          v-if="hasServices"
          class="lg-btn small ghost"
          data-test="stop-services"
          :disabled="checks.busy"
          @click="checks.stopServices()"
        >
          Stop check services
        </button>
        <button
          class="lg-btn small"
          data-test="suggest-checks"
          :disabled="checks.running || checks.busy"
          @click="checks.suggest(projectId)"
        >
          {{ approved ? "Suggest again" : "Suggest checks" }}
        </button>
      </span>
    </div>
    <p
      v-if="checks.stopped"
      class="small muted"
      role="status"
      data-test="services-stopped"
    >
      Check services stopped. They start again on the next run.
    </p>
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
      Reading the repository and dry-running the checks on the default branch…
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
      <div
        v-if="warnings.length"
        class="notice"
        role="status"
        data-test="proposal-warnings"
      >
        <div v-for="[k, w] in warnings" :key="k">
          <strong class="mono">{{ k }}</strong>: {{ w }}
        </div>
      </div>
      <div class="small muted">
        Proposed from {{ proposal.base_sha.slice(0, 7) }} · {{ proposal.model }}
      </div>
      <div class="pk-list" data-test="proposal">
        <p
          v-if="leaseError && firstPerRun < 0"
          class="notice"
          role="status"
          data-test="services-unavailable"
        >
          {{ leaseError }}
        </p>
        <template v-for="(s, i) in draft.setup ?? []" :key="'s' + i">
          <div
            class="pk-item"
            :data-test="s.per_run ? `per-run-${s.name}` : `setup-${s.name}`"
          >
            <p
              v-if="leaseError && i === firstPerRun"
              class="notice"
              role="status"
              data-test="services-unavailable"
            >
              {{ leaseError }}
            </p>
            <div class="row between wrap">
              <span
                ><strong>{{ s.name }}</strong>
                <span class="pill">{{
                  s.per_run ? "per run" : "setup"
                }}</span></span
              >
              <span v-if="!s.per_run" class="pill" :class="tone(s, 'setup')">{{
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
          class="pk-item"
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
            in {{ target(s)
            }}<template v-if="s.source"> · {{ s.source }}</template>
          </div>
          <div v-if="proposal.notes?.[s.name]" class="small muted">
            {{ proposal.notes[s.name] }}
          </div>
          <pre v-if="failsOnMain(proposal.results, s)" class="pk-out">{{
            tailOf(s)
          }}</pre>
        </div>
      </div>
      <div class="pk-item" data-test="smoke-edit">
        <label class="switch-row">
          <span class="grow"
            ><strong>Browser smoke test</strong>
            <span class="small muted"
              >Starts the app and opens the pages a change touches in the
              factory's own browser.</span
            ></span
          >
          <span class="switch"
            ><input
              type="checkbox"
              data-test="smoke-toggle"
              aria-label="Browser smoke test"
              :checked="!!smoke"
              :disabled="!session.canWrite"
              @change="
                checks.setSmoke(($event.target as HTMLInputElement).checked)
              " /><span></span
          ></span>
        </label>
        <template v-if="smoke">
          <p class="notice" data-test="smoke-warning">
            Env and login values here are handed to the app and the browser: use
            test values only (throwaway secrets, seed accounts), never real
            credentials.
          </p>
          <label class="row small">
            <input
              v-model="smoke.blocking"
              type="checkbox"
              data-test="smoke-blocking"
            />
            Blocking: a failed smoke test sends the work back to the Builder
          </label>
          <div
            v-for="(p, i) in smoke.start"
            :key="i"
            class="pk-item"
            :data-test="`smoke-proc-${i}`"
          >
            <div class="row between wrap">
              <input
                :value="p.name"
                aria-label="Process name"
                @input="checks.renameProcess(i, val($event))"
              />
              <button
                class="lg-btn small ghost"
                data-test="smoke-remove-proc"
                :disabled="smoke.start.length < 2"
                @click="checks.removeProcess(i)"
              >
                Remove
              </button>
            </div>
            <input
              v-model="p.command"
              class="mono"
              :aria-label="`${p.name} command`"
              placeholder="command"
            />
            <div class="row wrap">
              <input
                v-model="p.dir"
                :aria-label="`${p.name} dir`"
                placeholder="dir (repo root)"
              />
              <input
                v-model="p.port_env"
                :aria-label="`${p.name} port env`"
                placeholder="port env, e.g. PORT"
              />
              <input
                v-model="p.ready"
                :aria-label="`${p.name} ready path`"
                placeholder="ready path, e.g. /healthz"
              />
            </div>
            <textarea
              class="mono"
              rows="2"
              :value="mapToText(p.env)"
              :aria-label="`${p.name} env`"
              placeholder="KEY=VALUE per line; {{port.<process>}} allowed"
              @change="checks.setProcessEnv(i, val($event))"
            ></textarea>
          </div>
          <button
            class="lg-btn small ghost"
            data-test="smoke-add-proc"
            @click="checks.addProcess()"
          >
            Add process
          </button>
          <label class="small"
            >Base (the browser opens this process)
            <select v-model="smoke.base" aria-label="Smoke base">
              <option v-for="p in smoke.start" :key="p.name" :value="p.name">
                {{ p.name }}
              </option>
            </select>
          </label>
          <div class="row wrap">
            <input
              :value="smoke.login?.path ?? ''"
              aria-label="Login path"
              placeholder="login path, e.g. /login"
              @input="checks.setLogin('path', val($event))"
            />
            <input
              :value="smoke.login?.user_env ?? ''"
              aria-label="Login user env"
              placeholder="user env name"
              @input="checks.setLogin('user_env', val($event))"
            />
            <input
              :value="smoke.login?.password_env ?? ''"
              aria-label="Login password env"
              placeholder="password env name"
              @input="checks.setLogin('password_env', val($event))"
            />
          </div>
          <input
            v-model="smoke.routes_from"
            class="mono"
            aria-label="Routes from"
            placeholder="pages glob, e.g. web/app/**/page.tsx"
          />
          <textarea
            class="mono"
            rows="2"
            :value="mapToText(smoke.params)"
            aria-label="Route params"
            placeholder="param=value or param=sql:SELECT ... per line"
            @change="checks.setParams(val($event))"
          ></textarea>
          <label class="small"
            >Ready timeout (seconds, 0 = 90)
            <input
              v-model.number="smoke.ready_timeout_sec"
              type="number"
              min="0"
              max="600"
              aria-label="Ready timeout"
            />
          </label>
        </template>
      </div>
      <div v-if="services.length || env.length" class="small">
        <div v-if="services.length">
          <strong>Services</strong> from {{ draft.services?.compose }}:
          <span class="mono">{{ services.join(", ") }}</span>
        </div>
        <div v-if="env.length">
          <strong>Env the checks receive</strong>
          <div v-for="[k, v] in env" :key="k" class="mono">{{ k }}={{ v }}</div>
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
      <div class="pk-list" data-test="approved">
        <div
          v-for="(s, i) in approved.setup ?? []"
          :key="'s' + i"
          class="pk-item"
          :data-test="s.per_run ? `per-run-${s.name}` : `setup-${s.name}`"
        >
          <div class="row between wrap">
            <strong>{{ s.name }}</strong>
            <span class="pill">{{ s.per_run ? "per run" : "setup" }}</span>
          </div>
          <div class="pk-cmd">{{ s.command }}</div>
          <div class="pk-meta">in {{ target(s) }}</div>
        </div>
        <div v-for="(s, i) in approved.checks" :key="i" class="pk-item">
          <div class="row between wrap">
            <strong>{{ s.name }}</strong>
            <span v-if="s.optional" class="pill">optional</span>
          </div>
          <div class="pk-cmd">{{ s.command }}</div>
          <div class="pk-meta">
            in {{ target(s)
            }}<template v-if="s.source"> · {{ s.source }}</template>
          </div>
        </div>
        <div v-if="approved.smoke" class="pk-item" data-test="approved-smoke">
          <div class="row between wrap">
            <strong>Browser smoke test</strong>
            <span class="pill">{{
              approved.smoke.blocking ? "blocking" : "not blocking"
            }}</span>
          </div>
          <div
            v-for="p in approved.smoke.start"
            :key="p.name"
            class="mono small"
          >
            {{ p.name }}: {{ p.command }} · {{ p.port_env }} · {{ p.ready }}
          </div>
          <div class="small muted">
            opens {{ approved.smoke.base }} · pages from
            {{ approved.smoke.routes_from }}
            <template v-if="approved.smoke.login">
              · login {{ approved.smoke.login.path }}</template
            >
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
