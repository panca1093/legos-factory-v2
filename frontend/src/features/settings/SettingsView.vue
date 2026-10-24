<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { RouterLink, useRoute } from "vue-router";
import {
  ShieldCheck,
  UserRound,
  Users,
  Plug,
  Zap,
  Save,
  Lock,
  Check,
} from "lucide-vue-next";
import { useFactory } from "../../stores/factory";
import { useSession } from "../../stores/session";
import { useTheme } from "../../composables/useTheme";
import { money } from "../../composables/presentation";
import type { Settings } from "../../types/api";
import BrickBot from "../../components/agents/BrickBot.vue";
import IntegrationsPanel from "./IntegrationsPanel.vue";
const factory = useFactory(),
  session = useSession(),
  route = useRoute(),
  { theme, toggleTheme } = useTheme();
const config = ref<Settings>({
  budget_limit_micros: 0,
  architect_model: "",
  builder_model: "",
  inspector_model: "",
  allowed_models: null,
  cataloger_summaries: false,
});
const tabs = [
  { id: "governance", name: "Governance", icon: ShieldCheck },
  { id: "preferences", name: "My preferences", icon: UserRound },
  { id: "team", name: "Team & access", icon: Users },
  { id: "integrations", name: "Integrations", icon: Plug },
  { id: "usage", name: "Usage & resources", icon: Zap },
];
const tab = computed(() =>
  tabs.some((t) => t.id === route.query.tab)
    ? String(route.query.tab)
    : "governance",
);
const loading = ref(true),
  error = ref(""),
  saved = ref(false),
  busy = ref(false),
  initialized = ref(false),
  loadError = ref(false);
const dollars = computed({
  get: () => config.value.budget_limit_micros / 1000000,
  set: (v: number) => {
    config.value.budget_limit_micros = Math.round(Number(v) * 1000000);
  },
});
const catalog = computed(() =>
  factory.models.map((m) => ({
    id: `${m.provider_id}/${m.model_id}`,
    name: m.name,
  })),
);
async function load() {
  loading.value = true;
  error.value = "";
  loadError.value = false;
  const results = await Promise.allSettled([
    factory.loadSettings(),
    factory.refreshModels(),
    factory.refreshOverview(),
  ]);
  const settingsResult = results[0];
  initialized.value = settingsResult.status === "fulfilled";
  if (settingsResult.status === "fulfilled")
    config.value = settingsResult.value;
  const failures = results.filter((result) => result.status === "rejected");
  if (failures.length) {
    loadError.value = true;
    error.value = failures
      .map((result) =>
        result.reason instanceof Error
          ? result.reason.message
          : String(result.reason),
      )
      .join(" · ");
  }
  loading.value = false;
}
onMounted(load);
async function save() {
  if (!session.isAdmin || busy.value || !initialized.value) return;
  if (
    !Number.isFinite(config.value.budget_limit_micros) ||
    config.value.budget_limit_micros < 0
  ) {
    error.value = "Enter a valid nonnegative budget.";
    return;
  }
  busy.value = true;
  saved.value = false;
  error.value = "";
  try {
    await factory.saveSettings({
      ...config.value,
      allowed_models: config.value.allowed_models
        ? [...config.value.allowed_models]
        : null,
    });
    saved.value = true;
    await factory.refreshOverview();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
function toggleAllowed(id: string, checked: boolean) {
  const list = config.value.allowed_models ?? catalog.value.map((m) => m.id);
  config.value.allowed_models = checked
    ? [...new Set([...list, id])]
    : list.filter((m) => m !== id);
  saved.value = false;
}
</script>
<template>
  <div class="page-heading">
    <div>
      <div class="eyebrow">Make the factory work for your team</div>
      <h1>Workspace settings</h1>
      <p class="subtitle">Clear boundaries. Room to choose within them.</p>
    </div>
    <span class="pill purple">{{ session.user?.role }}</span>
  </div>
  <div class="settings-layout">
    <nav class="settings-nav" aria-label="Settings sections">
      <RouterLink
        v-for="item in tabs"
        :key="item.id"
        :to="{ path: '/settings', query: { tab: item.id } }"
        :class="{ active: tab === item.id }"
        ><component :is="item.icon" class="icon" />{{ item.name }}</RouterLink
      >
    </nav>
    <div class="detail-content">
      <div v-if="loading" class="panel empty" aria-busy="true">
        Loading workspace settings…
      </div>
      <template v-else
        ><div v-if="error" class="error-state" role="alert">
          {{ error
          }}<button v-if="loadError" class="btn small" @click="load">
            Try again
          </button>
        </div>
        <template v-if="tab === 'governance'"
          ><section class="settings-section">
            <h2>The crew that keeps the work honest.</h2>
            <p>
              Workspace defaults apply to new work orders. Foreman and Architect
              models are snapshotted when an order is created; Builder and
              Inspector can be chosen per order. Models come from OpenCode
              discovery, including subscription providers signed in there.
            </p>
            <div class="form-grid">
              <div
                v-for="role in [
                  'foreman',
                  'architect',
                  'builder',
                  'inspector',
                ] as const"
                :key="role"
                class="form-group"
              >
                <label :for="`set-${role}`" class="row"
                  ><BrickBot :persona="role" class="compact" /><span
                    style="text-transform: capitalize"
                    >{{ role }} default</span
                  ></label
                ><select
                  :id="`set-${role}`"
                  v-model="config[`${role}_model`]"
                  :disabled="!session.isAdmin || busy || !initialized"
                >
                  <option value="">Backend default</option>
                  <option
                    v-if="
                      config[`${role}_model`] &&
                      !catalog.some((m) => m.id === config[`${role}_model`])
                    "
                    :value="config[`${role}_model`]"
                  >
                    {{ config[`${role}_model`] }} · not currently discovered
                  </option>
                  <option v-for="m in catalog" :key="m.id" :value="m.id">
                    {{ m.name }}
                  </option>
                </select>
              </div>
            </div>
          </section>
          <section class="settings-section">
            <h2>Models your crew can choose.</h2>
            <p>
              Choose the catalog offered during work-order creation. Execution
              policy remains enforced by the backend.
            </p>
            <label
              v-for="m in factory.models"
              :key="m.provider_id + '/' + m.model_id"
              class="switch-row"
              ><span
                ><strong class="small">{{ m.name }}</strong
                ><span class="help" style="display: block"
                  >{{ m.provider_id }}/{{ m.model_id }} · {{ m.harness }}</span
                ></span
              ><span class="switch"
                ><input
                  type="checkbox"
                  :data-model="`${m.provider_id}/${m.model_id}`"
                  :aria-label="`Allow ${m.name}`"
                  :checked="
                    !config.allowed_models ||
                    config.allowed_models.includes(
                      `${m.provider_id}/${m.model_id}`,
                    )
                  "
                  :disabled="!session.isAdmin || busy || !initialized"
                  @change="
                    toggleAllowed(
                      `${m.provider_id}/${m.model_id}`,
                      ($event.target as HTMLInputElement).checked,
                    )
                  " /><span></span></span
            ></label>
            <p v-if="!factory.models.length" class="notice">
              No models discovered. Existing defaults are preserved.
            </p>
          </section>
          <section class="settings-section">
            <h2>Cataloger summaries</h2>
            <p>
              Knowledge is always recorded deterministically. Optionally, the
              Cataloger model writes one summary when a work order is approved;
              it is stored as an AI interpretation that cites its sources.
            </p>
            <label class="switch-row"
              ><span class="row"
                ><BrickBot persona="cataloger" class="compact" /><strong
                  class="small"
                  >Summarize completed work orders</strong
                ></span
              ><span class="switch"
                ><input
                  v-model="config.cataloger_summaries"
                  type="checkbox"
                  data-test="cataloger-summaries"
                  aria-label="Summarize completed work orders"
                  :disabled="
                    !session.isAdmin || busy || !initialized
                  " /><span></span></span
            ></label>
          </section>
          <section class="settings-section">
            <h2>Progressive autonomy</h2>
            <p>
              Orders can request guided, assisted or delegated execution.
              Workspace ceiling editing is unavailable.
            </p>
            <div class="autonomy-levels">
              <div
                v-for="(name, i) in ['Guided', 'Assisted', 'Delegated']"
                :key="name"
                class="level"
              >
                <b>{{ i + 1 }}. {{ name }}</b>
                <p>
                  {{
                    [
                      "Human approval before execution.",
                      "Execution after plan approval.",
                      "Execution under backend policy.",
                    ][i]
                  }}
                </p>
              </div>
            </div>
            <div class="notice section-gap">
              All modes retain backend state transitions, verification gates and
              a human PR disposition.
            </div>
          </section></template
        >
        <template v-else-if="tab === 'preferences'"
          ><section class="settings-section">
            <h2>Your crew preferences</h2>
            <p>
              Personal model overrides are unavailable. Choose Builder and
              Inspector models per work order.
            </p>
            <div class="form-grid">
              <div
                v-for="role in ['Foreman', 'Architect']"
                :key="role"
                class="form-group"
              >
                <label :for="`pref-${role}`">Preferred {{ role }}</label
                ><select :id="`pref-${role}`" disabled>
                  <option>
                    Workspace default · personal overrides unavailable
                  </option>
                </select>
              </div>
            </div>
          </section>
          <section class="settings-section">
            <h2>Make yourself at home.</h2>
            <div class="switch-row">
              <div>
                Dark theme
                <p class="help">Saved in this browser.</p>
              </div>
              <label class="switch"
                ><input
                  type="checkbox"
                  aria-label="Dark theme"
                  :checked="theme === 'dark'"
                  @change="toggleTheme" /><span></span
              ></label>
            </div></section
        ></template>
        <template v-else-if="tab === 'team'"
          ><section class="settings-section">
            <div class="row between">
              <div>
                <h2>A factory is a team effort.</h2>
                <p class="help">
                  Your current workspace identity and permissions.
                </p>
              </div>
              <button class="btn small" disabled>Add teammate</button>
            </div>
            <div class="user-row">
              <span class="avatar">{{
                session.user?.name
                  .split(" ")
                  .map((n) => n[0])
                  .slice(0, 2)
                  .join("")
              }}</span>
              <div class="grow">
                <strong class="small">{{ session.user?.name }} (you)</strong>
                <p class="help">{{ session.user?.email }}</p>
              </div>
              <span class="pill purple">{{ session.user?.role }}</span>
            </div>
            <div class="notice">
              Membership lists, invitations, role changes and per-user budgets
              are unavailable. Your actual session role controls API access.
            </div>
          </section></template
        >
        <template v-else-if="tab === 'integrations'"
          ><IntegrationsPanel
        /></template>
        <template v-else
          ><section class="settings-section">
            <div class="row between">
              <div>
                <h2>Room to build. Limits that hold.</h2>
                <p class="help">Workspace usage reported by the backend.</p>
              </div>
              <span class="pill purple">Current totals</span>
            </div>
            <div class="readout">
              <div>
                <b>{{ money(factory.overview?.usage.cost_micros) }}</b
                ><span>recorded execution cost</span>
              </div>
              <div>
                <b>{{ factory.overview?.usage.tokens_in ?? 0 }}</b
                ><span
                  >input tokens ·
                  {{ factory.overview?.usage.tokens_out ?? 0 }} output
                  tokens</span
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
              Scripted mock estimates and real harness accounting retain their
              run labels. These totals are not a provider invoice.
            </p>
          </section>
          <section class="settings-section">
            <h2>Budget & execution limits</h2>
            <p>
              The budget cap is writable by workspace admins. Zero leaves it
              unset.
            </p>
            <div class="form-grid">
              <div class="form-group">
                <label for="set-budget">Workspace budget cap (USD)</label
                ><input
                  id="set-budget"
                  v-model="dollars"
                  type="number"
                  min="0"
                  step="0.01"
                  :disabled="!session.isAdmin || busy || !initialized"
                />
              </div>
              <div class="form-group">
                <label for="set-concurrency">Concurrent run slots</label
                ><input id="set-concurrency" value="Not reported" disabled />
              </div>
              <div class="form-group">
                <label for="set-cpu">vCPU per run</label
                ><input id="set-cpu" value="Not reported" disabled />
              </div>
              <div class="form-group">
                <label for="set-memory">Memory per run</label
                ><input id="set-memory" value="Not reported" disabled />
              </div>
            </div>
            <div class="notice section-gap">
              Resource limits, capacity editing and host telemetry are
              unavailable through the current API.
            </div>
          </section></template
        >
        <div v-if="['governance', 'usage'].includes(tab)" class="form-actions">
          <button
            class="btn primary"
            data-test="save-settings"
            :disabled="busy || !session.isAdmin || !initialized"
            @click="save"
          >
            <Save class="icon" />{{
              busy ? "Saving…" : "Save settings"
            }}</button
          ><span
            v-if="saved"
            class="small status-line"
            style="color: var(--green)"
            role="status"
            ><Check class="icon" />Settings saved</span
          ><span v-if="!session.isAdmin" class="tiny muted status-line"
            ><Lock class="icon" />Admin access required</span
          >
        </div></template
      >
    </div>
  </div>
</template>
