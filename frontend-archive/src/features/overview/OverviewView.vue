<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useFactory } from '../../stores/factory'
import { subscribe } from '../../services/events/sse'
import BrickBot from '../../components/ui/BrickBot.vue'
import { Hammer, CircleCheck, Clock3, TriangleAlert, Zap } from 'lucide-vue-next'
import { useRouter } from 'vue-router'

const factory = useFactory()
const router = useRouter()
const live = ref('')

const crew = [
  { persona: 'foreman', name: 'Foreman', role: 'Orchestration' },
  { persona: 'architect', name: 'Architect', role: 'Planning & review' },
  { persona: 'builder', name: 'Builder', role: 'Implementation' },
  { persona: 'inspector', name: 'Inspector', role: 'Verification' },
  { persona: 'cataloger', name: 'Cataloger', role: 'Experimental' },
] as const

onMounted(async () => {
  await factory.refreshOverview()
  subscribe('/api/v1/events', (ev) => {
    live.value = `${ev.actor}: ${ev.message}`
  })
})

function fmtMicros(n: number): string {
  if (n <= 0) return '—'
  return `$${(n / 1_000_000).toFixed(4)}`
}
function budgetPct(): number {
  const b = factory.overview?.budget
  if (!b || !b.limit_micros) return 0
  return Math.min(100, Math.round((b.spent_micros / b.limit_micros) * 100))
}
</script>

<template>
  <div class="mx-auto max-w-5xl">
    <Transition name="fade" appear>
      <div>
        <h1 class="text-4xl" style="font-family: var(--serif)">
          Hello, {{ factory.overview?.user?.name?.split(' ')[0] ?? 'there' }}
          <span class="greeting-icon" aria-hidden="true">🧱</span>
        </h1>
        <p class="mt-1" style="color: var(--muted)">
          The crew is on the floor. Here is what matters right now.
        </p>

        <!-- crew -->
        <div class="panel mt-6 flex flex-wrap items-center justify-between gap-4 p-5">
          <div class="flex items-center gap-5 overflow-x-auto">
            <div v-for="c in crew" :key="c.persona" class="flex flex-col items-center gap-1 text-center">
              <BrickBot :persona="c.persona" :size="52" />
              <div class="text-xs font-extrabold">{{ c.name }}</div>
              <div class="text-[10px]" style="color: var(--muted)">{{ c.role }}</div>
            </div>
          </div>
          <div class="min-w-40 max-w-64 text-xs" style="color: var(--muted)" aria-live="polite">
            <div class="eyebrow mb-1">Live floor</div>
            <div v-if="live" class="mono truncate">{{ live }}</div>
            <div v-else>Quiet for now — work orders stream their activity in Factory.</div>
          </div>
        </div>

        <!-- metrics -->
        <div class="mt-6 grid grid-cols-2 gap-4 md:grid-cols-3">
          <RouterLink to="/factory" class="panel p-5 no-underline" style="color: var(--ink)">
            <div class="flex items-center gap-2 text-xs font-bold" style="color: var(--muted)"><Hammer :size="14" /> Active work orders</div>
            <div class="mt-2 text-3xl font-extrabold">{{ factory.overview?.metrics.active_work_orders ?? 0 }}</div>
          </RouterLink>
          <RouterLink to="/factory" class="panel p-5 no-underline" style="color: var(--ink)">
            <div class="flex items-center gap-2 text-xs font-bold" style="color: var(--muted)"><Clock3 :size="14" /> Waiting for you</div>
            <div class="mt-2 text-3xl font-extrabold" style="color: var(--orange)">{{ factory.overview?.metrics.waiting_for_human ?? 0 }}</div>
          </RouterLink>
          <RouterLink to="/factory" class="panel p-5 no-underline" style="color: var(--ink)">
            <div class="flex items-center gap-2 text-xs font-bold" style="color: var(--muted)"><TriangleAlert :size="14" /> Failed jobs</div>
            <div class="mt-2 text-3xl font-extrabold" style="color: var(--red)">{{ factory.overview?.metrics.failed_jobs ?? 0 }}</div>
          </RouterLink>
          <div class="panel p-5">
            <div class="flex items-center gap-2 text-xs font-bold" style="color: var(--muted)"><Zap :size="14" /> Running tasks</div>
            <div class="mt-2 text-3xl font-extrabold">{{ factory.overview?.metrics.pending_jobs ?? 0 }}</div>
          </div>
          <div class="panel p-5">
            <div class="flex items-center gap-2 text-xs font-bold" style="color: var(--muted)"><CircleCheck :size="14" /> Projects</div>
            <div class="mt-2 text-3xl font-extrabold">{{ factory.overview?.metrics.projects ?? 0 }}</div>
          </div>
          <div class="panel p-5">
            <div class="text-xs font-bold" style="color: var(--muted)">Usage & budget</div>
            <div class="mt-2 mono text-lg font-bold">{{ fmtMicros(factory.overview?.usage.cost_micros ?? 0) }} spent</div>
            <div class="mt-2 h-2 overflow-hidden rounded-full" style="background: var(--panel2)">
              <div class="h-full rounded-full" :style="{ width: budgetPct() + '%', background: 'var(--violet)' }"></div>
            </div>
            <div class="mt-1 text-[10px]" style="color: var(--muted)">
              {{ budgetPct() }}% of configured cap · estimates, not billing
            </div>
          </div>
        </div>

        <p class="mt-6 text-xs" style="color: var(--muted)">
          <button class="btn small subtle" @click="router.push('/factory')">Open the Factory floor →</button>
        </p>
      </div>
    </Transition>
  </div>
</template>
