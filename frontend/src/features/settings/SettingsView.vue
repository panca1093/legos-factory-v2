<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { Lock } from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import { money } from "../../composables/presentation";
import type { RuntimeInfo, Settings } from "../../types/api";
import AgentsSection from "./AgentsSection.vue";
import ConnectionsSection from "./ConnectionsSection.vue";

const factory = useFactory(),
  session = useSession(),
  route = useRoute();
const config = ref<Settings>({
  budget_limit_micros: 0,
  architect_model: "",
  builder_model: "",
  inspector_model: "",
  allowed_models: null,
});
const runtime = ref<RuntimeInfo | null>(null);
const loading = ref(true),
  initialized = ref(false),
  error = ref(""),
  budgetError = ref(""),
  budgetBusy = ref(false);

const legacyTabs: Record<string, string> = {
  governance: "agents",
  integrations: "connections",
  usage: "budget",
};
const target = computed(() => {
  const hash = route.hash.replace(/^#/, "");
  return hash || legacyTabs[String(route.query.tab ?? "")] || "";
});
async function scrollToTarget() {
  await nextTick();
  if (target.value)
    document.getElementById(target.value)?.scrollIntoView?.({ block: "start" });
}

const dollars = computed({
  get: () => config.value.budget_limit_micros / 1000000,
  set: (v: number) => {
    config.value.budget_limit_micros = Math.round(Number(v) * 1000000);
  },
});

async function load() {
  loading.value = true;
  error.value = "";
  const [settings, rt, ...rest] = await Promise.allSettled([
    factory.loadSettings(),
    factory.loadRuntime(),
    factory.refreshModels(),
    factory.refreshOverview(),
  ]);
  initialized.value = settings.status === "fulfilled";
  if (settings.status === "fulfilled") config.value = settings.value;
  if (rt.status === "fulfilled") runtime.value = rt.value;
  error.value = [settings, rt, ...rest]
    .filter((r) => r.status === "rejected")
    .map((r) =>
      r.reason instanceof Error ? r.reason.message : String(r.reason),
    )
    .join(" · ");
  loading.value = false;
  void scrollToTarget();
}
onMounted(load);
watch(target, scrollToTarget);

async function saveBudget() {
  if (!session.isAdmin || budgetBusy.value || !initialized.value) return;
  const micros = config.value.budget_limit_micros;
  if (!Number.isFinite(micros) || micros < 0) {
    budgetError.value = "Enter a valid nonnegative budget.";
    return;
  }
  budgetBusy.value = true;
  budgetError.value = "";
  try {
    const saved = await factory.patchSettings({ budget_limit_micros: micros });
    config.value.budget_limit_micros = saved.budget_limit_micros;
    await factory.refreshOverview();
  } catch (e) {
    budgetError.value = e instanceof Error ? e.message : String(e);
  } finally {
    budgetBusy.value = false;
  }
}

const harnessLine = computed(() => {
  const r = runtime.value;
  if (!r) return "";
  if (r.harness === "acp") return `ACP agent · ${r.acp_command}`;
  if (r.harness === "opencode") return "OpenCode server";
  return "Scripted mock (no real agent)";
});
const harnessEnv = computed(() =>
  runtime.value?.harness === "opencode"
    ? "FACTORY_OPENCODE_URL"
    : "FACTORY_HARNESS, FACTORY_ACP_COMMAND",
);
</script>
<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">Make the factory work for your team</div>
      <h1>Workspace settings</h1>
      <p class="subtitle">Applies to everyone in this workspace.</p>
    </div>
    <span class="pill purple">{{ session.user?.role }}</span>
  </div>
  <div class="detail-content" style="max-width: 800px">
    <div v-if="loading" class="panel empty" aria-busy="true">
      Loading workspace settings…
    </div>
    <template v-else>
      <div v-if="error" class="error-state" role="alert">
        {{ error }}<button class="btn small" @click="load">Try again</button>
      </div>
      <p v-if="!session.isAdmin" class="notice" data-test="read-only-note">
        <Lock class="icon" /> You can look but not change workspace defaults.
        Ask an admin to change them.
      </p>

      <AgentsSection
        :settings="config"
        :editable="session.isAdmin && initialized"
        @saved="Object.assign(config, $event)"
      />
      <ConnectionsSection />

      <section id="runtime" class="settings-section" data-test="runtime">
        <h2>Runtime</h2>
        <p>
          What this backend is running with. These are set in the server's
          environment, not here.
        </p>
        <template v-if="runtime">
          <div class="switch-row" data-test="runtime-harness">
            <div>
              Agent harness
              <p class="help">{{ harnessLine }}</p>
            </div>
            <span class="tiny muted">{{ harnessEnv }}</span>
          </div>
          <div class="switch-row" data-test="runtime-jev">
            <div>
              Jev judgments
              <p class="help">
                {{
                  runtime.jev.enabled
                    ? `On · ${runtime.jev.model}. Plans that pass the checks start building without a click.`
                    : "Off. Every plan waits for your approval and the final done check is skipped."
                }}
              </p>
            </div>
            <span class="tiny muted">TYPESAFE_API_KEY, TYPESAFE_MODEL</span>
          </div>
          <div class="switch-row" data-test="runtime-credentials">
            <div>
              Credential storage
              <p class="help">
                {{
                  runtime.credentials_configured
                    ? "Configured."
                    : "Not configured. Connections that need a token cannot be added."
                }}
              </p>
            </div>
            <span class="tiny muted">FACTORY_SECRET_KEY</span>
          </div>
          <div class="switch-row" data-test="runtime-runs">
            <div>
              Concurrent runs
              <p class="help">{{ runtime.max_concurrent_runs }}</p>
            </div>
            <span class="tiny muted">FACTORY_MAX_CONCURRENT_RUNS</span>
          </div>
        </template>
        <p v-else class="help">Runtime details are unavailable.</p>
      </section>

      <section id="budget" class="settings-section">
        <h2>Budget</h2>
        <p>
          Workspace usage reported by the backend. Zero leaves the cap unset.
        </p>
        <div class="readout">
          <div>
            <b>{{ money(factory.overview?.usage.cost_micros) }}</b
            ><span>recorded execution cost</span>
          </div>
          <div>
            <b>{{ factory.overview?.usage.tokens_in ?? 0 }}</b
            ><span
              >input tokens ·
              {{ factory.overview?.usage.tokens_out ?? 0 }} output tokens</span
            >
          </div>
          <div>
            <b>{{ money(factory.overview?.budget.reserved_micros) }}</b
            ><span>reserved budget</span>
          </div>
          <div>
            <b>{{
              factory.overview?.budget.limit_micros
                ? money(factory.overview.budget.available_micros)
                : "No cap"
            }}</b
            ><span>available budget</span>
          </div>
        </div>
        <p class="help">
          Scripted mock estimates and real harness accounting retain their run
          labels. These totals are not a provider invoice.
        </p>
        <div class="form-group" style="max-width: 280px">
          <label for="set-budget">Workspace budget cap (USD)</label
          ><input
            id="set-budget"
            v-model="dollars"
            type="number"
            min="0"
            step="0.01"
            :disabled="!session.isAdmin || budgetBusy || !initialized"
            @change="saveBudget"
          />
        </div>
        <p v-if="budgetError" class="form-error" role="alert">
          {{ budgetError }}
        </p>
      </section>
    </template>
  </div>
</template>
