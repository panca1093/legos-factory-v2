<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useFactory } from '../../stores/factory'
import { useSession } from '../../stores/session'
import type { Settings } from '../../types/api'
import { ShieldCheck, Save } from 'lucide-vue-next'

const factory = useFactory()
const session = useSession()

const settings = ref<Settings>({
  budget_limit_micros: 0,
  architect_model: '',
  builder_model: '',
  inspector_model: '',
  allowed_models: null,
})
const saved = ref(false)
const saveError = ref('')
const loading = ref(true)

onMounted(async () => {
  await Promise.all([factory.refreshModels(), factory.refreshOverview()])
  try {
    settings.value = await factory.loadSettings()
  } finally {
    loading.value = false
  }
})

async function save() {
  saveError.value = ''
  saved.value = false
  try {
    await factory.saveSettings(settings.value)
    saved.value = true
    setTimeout(() => (saved.value = false), 2000)
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e)
  }
}

function fmtMicros(n: number): string {
  return (n / 1_000_000).toFixed(2)
}
function budgetDollars(v: number): number {
  return Math.round((v / 1_000_000) * 10000) / 10000
}
</script>

<template>
  <div class="mx-auto max-w-4xl">
    <h1 class="text-2xl font-extrabold">Settings</h1>
    <p class="mt-1 text-sm" style="color: var(--muted)">Governance is admin-only. Model defaults apply to future work orders.</p>

    <div v-if="loading" class="panel mt-6 p-10 text-center text-sm" style="color: var(--muted)">Loading…</div>

    <div v-else class="mt-6 grid gap-4 md:grid-cols-2">
      <!-- governance -->
      <div class="panel p-5">
        <div class="flex items-center gap-2 eyebrow"><ShieldCheck :size="14" /> Workspace governance</div>
        <div class="mt-3 flex flex-col gap-3">
          <div>
            <label class="label" for="set-budget">Monthly budget cap (USD, 0 = unset)</label>
            <input
              id="set-budget"
              class="input mt-1"
              type="number"
              :value="budgetDollars(settings.budget_limit_micros)"
              :disabled="!session.isAdmin"
              min="0"
              step="10"
              @input="settings.budget_limit_micros = Math.round(Number(($event.target as HTMLInputElement).value) * 1_000_000)"
            />
            <div class="mt-1 text-[10px]" style="color: var(--muted)">
              Current: {{ fmtMicros(factory.overview?.budget.spent_micros ?? 0) }} spent · local estimate-based enforcement
            </div>
          </div>
          <p v-if="!session.isAdmin" class="text-xs font-bold" style="color: var(--orange)">Admin role required to change governance.</p>
        </div>
      </div>

      <!-- models -->
      <div class="panel p-5">
        <div class="eyebrow">Model defaults</div>
        <p class="mt-1 text-[11px]" style="color: var(--muted)">
          Discovered models: {{ factory.models.length }} · harness: {{ factory.models[0]?.harness ?? 'none' }}
        </p>
        <div class="mt-3 flex flex-col gap-3">
          <div v-for="role in (['architect', 'builder', 'inspector'] as const)" :key="role">
            <label class="label" :for="`set-${role}`">{{ role }} default (provider/model)</label>
            <input
              :id="`set-${role}`"
              class="input mt-1"
              :disabled="!session.isAdmin"
              placeholder="provider/model or empty"
              :value="role === 'architect' ? settings.architect_model : role === 'builder' ? settings.builder_model : settings.inspector_model"
              @input="role === 'architect' ? (settings.architect_model = ($event.target as HTMLInputElement).value) : role === 'builder' ? (settings.builder_model = ($event.target as HTMLInputElement).value) : (settings.inspector_model = ($event.target as HTMLInputElement).value)"
            />
          </div>
        </div>
      </div>

      <!-- integrations -->
      <div class="panel p-5 md:col-span-2">
        <div class="eyebrow">Integrations</div>
        <div class="mt-2 grid gap-3 text-xs md:grid-cols-2" style="color: var(--muted)">
          <div class="rounded-xl p-3" style="background: var(--panel2)">
            <div class="font-bold" style="color: var(--ink)">OpenCode agent harness</div>
            Configured via <span class="mono">FACTORY_OPENCODE_URL</span> on the backend.
            {{ factory.models.some((m) => m.harness === 'opencode') ? 'Connected — models discovered.' : 'Not configured — the scripted mock harness runs instead (runs are labeled mock).' }}
          </div>
          <div class="rounded-xl p-3" style="background: var(--panel2)">
            <div class="font-bold" style="color: var(--ink)">GitHub / GitLab / Jira</div>
            Adapter foundations only — <strong>unverified</strong>: no credentials are held by this build and no external calls are made. PRs are never published automatically.
          </div>
        </div>
      </div>

      <div class="md:col-span-2 flex items-center gap-3">
        <button class="btn primary" :disabled="!session.isAdmin" @click="save"><Save :size="14" /> Save settings</button>
        <span v-if="saved" class="text-xs font-bold" style="color: var(--green)" role="status">Saved ✓</span>
        <span v-if="saveError" class="text-xs font-bold" style="color: var(--red)" role="alert">{{ saveError }}</span>
      </div>
    </div>
  </div>
</template>
