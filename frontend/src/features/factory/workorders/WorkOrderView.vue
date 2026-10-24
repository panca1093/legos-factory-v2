<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useFactory } from '../../../stores/factory'
import { useSession } from '../../../stores/session'
import { subscribe } from '../../../services/events/sse'
import StatusBadge from '../../../components/ui/StatusBadge.vue'
import { ArrowLeft, Ban, Download, CircleAlert } from 'lucide-vue-next'

const factory = useFactory()
const session = useSession()
const route = useRoute()
const router = useRouter()
const woID = route.params.id as string

const tab = ref<'blueprint' | 'graph' | 'activity' | 'quality' | 'changes'>('blueprint')
const tabs = [
  { key: 'blueprint', label: 'Blueprint' },
  { key: 'graph', label: 'Task graph' },
  { key: 'activity', label: 'Activity' },
  { key: 'quality', label: 'Quality' },
  { key: 'changes', label: 'Changes & PR' },
] as const

let unsub: (() => void) | null = null
onMounted(async () => {
  await factory.loadDetail(woID)
  unsub = subscribe(`/api/v1/work-orders/${woID}/events`, async () => {
    await factory.reloadDetail().catch(() => {})
  })
})
onBeforeUnmount(() => unsub?.())

const wo = computed(() => factory.detail?.work_order)
const tasks = computed(() => factory.detail?.plan ?? [])
const runs = computed(() => factory.detail?.runs ?? [])

// clarification / actions
const clarifAnswer = ref('')
const busy = ref(false)
const actionError = ref('')
async function act(fn: () => Promise<unknown>) {
  busy.value = true
  actionError.value = ''
  try {
    await fn()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

const draftPlan = () => act(() => factory.planGenerate(woID))
const approve = () => act(() => factory.planDecision(woID, 'approve'))
const revise = () => act(() => factory.planDecision(woID, 'revise', 'Scope needs tightening'))
const answer = () => act(() => factory.answerClarification(woID, clarifAnswer.value))
const cancel = () => act(() => factory.cancel(woID))
const prApprove = () => act(() => factory.prDecision(woID, 'approve', 'Evidence complete'))
const prRequestChanges = () => act(() => factory.prDecision(woID, 'request_changes', 'Diff needs adjustment'))
const prDecline = () => act(() => factory.prDecision(woID, 'decline', 'Not the right outcome'))

// task states from runs
function taskState(taskID: string): string {
  const r = runs.value.filter((r) => r.task_id === taskID).at(-1)
  return r ? r.state : 'pending'
}

// DAG layout: simple layered positions by dependency depth
const graphNodes = computed(() => {
  const depth: Record<string, number> = {}
  for (const t of tasks.value) {
    depth[t.id] = 0
  }
  for (let i = 0; i < tasks.value.length; i++) {
    for (const t of tasks.value) {
      const maxDep = Math.max(0, ...(t.deps ?? []).map((d) => depth[d] ?? 0))
      if (maxDep >= depth[t.id]) depth[t.id] = maxDep + 1
    }
  }
  const layers: Record<number, string[]> = {}
  for (const t of tasks.value) {
    ;(layers[depth[t.id]] ??= []).push(t.id)
  }
  return tasks.value.map((t) => {
    const layer = depth[t.id]
    const idx = layers[layer].indexOf(t.id)
    return {
      ...t,
      x: 40 + layer * 190,
      y: 50 + idx * 84,
      state: taskState(t.id),
    }
  })
})
const graphEdges = computed(() => {
  const byID = new Map(graphNodes.value.map((n) => [n.id, n]))
  const edges: { x1: number; y1: number; x2: number; y2: number }[] = []
  for (const n of graphNodes.value) {
    for (const d of n.deps ?? []) {
      const dep = byID.get(d)
      if (dep) edges.push({ x1: dep.x + 130, y1: dep.y + 28, x2: n.x, y2: n.y + 28 })
    }
  }
  return edges
})
const graphWidth = computed(() => Math.max(560, 200 + graphNodes.value.length * 190))

const stateEvents = computed(() => factory.events.filter((e) => e.kind !== 'agent_started'))

// quality grouping
const checksByRevision = computed(() => {
  const groups: Record<number, typeof factory.quality> = {}
  for (const c of factory.quality) {
    ;(groups[c.revision] ??= []).push(c)
  }
  return Object.entries(groups).map(([rev, checks]) => ({ revision: Number(rev), checks })).sort((a, b) => b.revision - a.revision)
})

// changes: latest builder outputs + diff-like summary from builder runs
const builderRuns = computed(() => runs.value.filter((r) => r.kind === 'builder'))

function blueprintMarkdown(): string {
  const w = wo.value
  if (!w) return ''
  return [
    `# Blueprint: ${w.title}`,
    '',
    `**Work order:** ${w.id}  `,
    `**State:** ${w.state}  `,
    `**Autonomy:** ${w.autonomy}  `,
    `**Revision:** ${w.revision}`,
    '',
    '## Objective',
    w.objective,
    '',
    '## Acceptance criteria',
    w.acceptance || '(none recorded)',
    '',
    '## Plan',
    ...tasks.value.map((t) => `- **${t.title}** (${t.role}) → ${t.objective} ${((t.deps ?? []).length ? `(after: ${t.deps.join(', ')})` : '')}`),
    '',
    w.clarif_question ? `## Clarification\nQ: ${w.clarif_question}\nA: ${w.clarif_answer || '(unanswered)'}\n` : '',
  ].join('\n')
}

function download(name: string, content: string) {
  const blob = new Blob([content], { type: 'text/markdown' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

const canWrite = () => session.canWrite
const canCancel = computed(() => wo.value && !['READY', 'CANCELLED', 'BLOCKED'].includes(wo.value.state))
</script>

<template>
  <div v-if="factory.error" class="panel p-6 text-center">
    <p class="font-bold" style="color: var(--red)">{{ factory.error }}</p>
    <button class="btn mt-3" @click="router.push('/factory')">← Back to Factory</button>
  </div>

  <div v-else-if="!wo" class="panel p-10 text-center text-sm" style="color: var(--muted)">Loading work order…</div>

  <div v-else>
    <button class="btn small mb-3" @click="router.push('/factory')"><ArrowLeft :size="14" /> Factory</button>

    <div class="panel p-5">
      <div class="flex flex-wrap items-center gap-3">
        <h1 class="text-2xl font-extrabold">{{ wo.title }}</h1>
        <StatusBadge :state="wo.state" />
        <span class="badge">rev {{ wo.revision }}</span>
        <span class="badge violet">{{ wo.autonomy }}</span>
        <div class="ml-auto flex gap-2">
          <button v-if="canWrite() && canCancel" class="btn small danger" :disabled="busy" @click="cancel"><Ban :size="13" /> Cancel</button>
        </div>
      </div>
      <p v-if="actionError" class="mt-2 text-xs font-bold" style="color: var(--red)" role="alert">{{ actionError }}</p>
    </div>

    <!-- tabs -->
    <div class="mt-4 flex gap-1 overflow-x-auto rounded-2xl p-1" style="background: var(--panel2)" role="tablist" aria-label="Work order sections">
      <button
        v-for="t in tabs"
        :key="t.key"
        role="tab"
        :aria-selected="tab === t.key"
        class="whitespace-nowrap rounded-xl px-4 py-2 text-xs font-extrabold"
        :style="tab === t.key ? 'background: var(--panel); color: var(--violet); box-shadow: var(--shadow)' : 'color: var(--muted)'"
        @click="tab = t.key"
      >
        {{ t.label }}
      </button>
    </div>

    <Transition name="fade" mode="out-in">
      <!-- BLUEPRINT -->
      <div v-if="tab === 'blueprint'" key="bp" class="mt-4 grid gap-4 lg:grid-cols-3">
        <div class="panel p-5 lg:col-span-2">
          <div class="eyebrow">Intent</div>
          <p class="mt-2 whitespace-pre-wrap">{{ wo.objective }}</p>
          <div class="eyebrow mt-5">Acceptance criteria</div>
          <p class="mt-2 whitespace-pre-wrap">{{ wo.acceptance || '(none recorded)' }}</p>

          <div class="eyebrow mt-5">Plan</div>
          <div v-if="tasks.length === 0" class="mt-2 text-xs" style="color: var(--muted)">
            No plan drafted yet.
            <button v-if="canWrite() && wo.state === 'CREATED'" class="btn small primary ml-2" :disabled="busy" @click="draftPlan">
              Draft blueprint
            </button>
          </div>
          <ol v-else class="mt-2 flex flex-col gap-2">
            <li v-for="t in tasks" :key="t.id" class="rounded-xl p-3" style="background: var(--panel2)">
              <div class="flex items-center gap-2 text-sm font-bold">{{ t.title }} <span class="badge violet">{{ t.role }}</span></div>
              <div class="text-xs" style="color: var(--muted)">{{ t.objective }}</div>
              <div v-if="t.deps?.length" class="mt-1 text-[10px] mono" style="color: var(--muted)">after: {{ t.deps.join(', ') }}</div>
            </li>
          </ol>
        </div>

        <div class="flex flex-col gap-4">
          <!-- clarification -->
          <div v-if="wo.clarif_question" class="panel p-5">
            <div class="flex items-center gap-2 eyebrow" style="color: var(--orange)"><CircleAlert :size="14" /> Clarification</div>
            <p class="mt-2 text-sm">{{ wo.clarif_question }}</p>
            <template v-if="wo.clarif_answer">
              <div class="eyebrow mt-3">Recorded answer</div>
              <p class="mt-1 text-sm" style="color: var(--green)">{{ wo.clarif_answer }}</p>
            </template>
            <template v-else-if="canWrite()">
              <label class="label mt-3" for="clarif">Your answer</label>
              <textarea id="clarif" v-model="clarifAnswer" class="input mt-1" rows="2" placeholder="Record the decision…"></textarea>
              <button class="btn primary mt-2 w-full justify-center" :disabled="busy || !clarifAnswer.trim()" @click="answer">Answer & re-review</button>
            </template>
          </div>

          <!-- decisions -->
          <div class="panel p-5">
            <div class="eyebrow">Human decision</div>
            <p class="mt-1 text-xs" style="color: var(--muted)">State: {{ wo.state }}</p>
            <div class="mt-3 flex flex-col gap-2">
              <button v-if="wo.state === 'ARCHITECT_REVIEW'" class="btn primary justify-center" :disabled="busy" @click="approve">Approve plan →</button>
              <button v-if="wo.state === 'ARCHITECT_REVIEW'" class="btn justify-center" :disabled="busy" @click="revise">Request revision</button>
              <button v-if="wo.state === 'NEEDS_HUMAN_REVIEW'" class="btn primary justify-center" :disabled="busy" @click="prApprove">Approve PR</button>
              <button v-if="wo.state === 'NEEDS_HUMAN_REVIEW'" class="btn justify-center" :disabled="busy" @click="prRequestChanges">Request changes</button>
              <button v-if="wo.state === 'NEEDS_HUMAN_REVIEW' || wo.state === 'BLOCKED'" class="btn danger justify-center" :disabled="busy" @click="prDecline">Decline</button>
              <div v-if="['CREATED','WAITING_FOR_HUMAN','ARCHITECT_REVIEW','PLANNING'].includes(wo.state)" class="text-xs" style="color: var(--muted)">
                Waiting on the steps above.
              </div>
              <div v-else class="text-xs" style="color: var(--muted)">Execution in progress — watch Activity.</div>
            </div>
          </div>

          <div class="panel p-5">
            <div class="eyebrow">Export</div>
            <button class="btn small mt-2" @click="download(`${wo.id}-blueprint.md`, blueprintMarkdown())">
              <Download :size="13" /> Blueprint markdown
            </button>
          </div>
        </div>
      </div>

      <!-- TASK GRAPH -->
      <div v-else-if="tab === 'graph'" key="gr" class="panel mt-4 overflow-x-auto p-5">
        <svg :width="graphWidth" :height="Math.max(220, 120 + graphNodes.length * 84)" role="img" aria-label="Task dependency graph">
          <line v-for="(e, i) in graphEdges" :key="i" :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
            stroke="var(--line)" stroke-width="2" />
          <g v-for="n in graphNodes" :key="n.id" :transform="`translate(${n.x}, ${n.y})`">
            <rect width="130" height="56" rx="12"
              :fill="n.state === 'completed' ? 'var(--mint)' : n.state === 'running' ? 'var(--violet-soft)' : 'var(--panel2)'"
              :stroke="n.state === 'completed' ? 'var(--green)' : n.state === 'running' ? 'var(--violet)' : 'var(--line)'" />
            <text x="10" y="22" font-size="12" font-weight="700" fill="var(--ink)">{{ n.title.slice(0, 22) }}</text>
            <text x="10" y="40" font-size="10" fill="var(--muted)">{{ n.state }}</text>
          </g>
        </svg>
      </div>

      <!-- ACTIVITY -->
      <div v-else-if="tab === 'activity'" key="ac" class="panel mt-4 p-5">
        <TransitionGroup name="brick" tag="div" class="flex flex-col gap-2">
          <div v-for="e in stateEvents" :key="e.id" class="flex items-start gap-3 rounded-xl p-3" style="background: var(--panel2)">
            <span class="badge" :class="{ violet: e.actor === 'architect', mint: e.actor === 'inspector', amber: e.actor === 'builder' }">{{ e.actor }}</span>
            <div class="min-w-0 flex-1">
              <div class="text-sm">{{ e.message }}</div>
              <div class="text-[10px]" style="color: var(--muted)">{{ e.kind }} · {{ new Date(e.created_at).toLocaleTimeString() }}</div>
            </div>
          </div>
        </TransitionGroup>
        <div v-if="stateEvents.length === 0" class="py-8 text-center text-xs" style="color: var(--muted)">No activity yet.</div>
      </div>

      <!-- QUALITY -->
      <div v-else-if="tab === 'quality'" key="qu" class="mt-4 flex flex-col gap-4">
        <div v-for="g in checksByRevision" :key="g.revision" class="panel p-5">
          <div class="eyebrow mb-3">Revision {{ g.revision }}</div>
          <div class="flex flex-col gap-2">
            <div v-for="c in g.checks" :key="c.id" class="rounded-xl p-3" style="background: var(--panel2)">
              <div class="flex items-center gap-2">
                <span class="badge" :class="c.outcome === 'PASS' ? 'mint' : c.outcome === 'FAIL' ? 'red' : 'amber'">{{ c.outcome }}</span>
                <span class="text-sm font-bold">{{ c.name }}</span>
                <span class="mono text-[11px]" style="color: var(--muted)">{{ c.command }}</span>
              </div>
              <details v-if="c.output" class="mt-1">
                <summary class="cursor-pointer text-[11px]" style="color: var(--muted)">evidence output</summary>
                <pre class="mono mt-1 max-h-40 overflow-auto whitespace-pre-wrap text-[11px]" style="color: var(--muted)">{{ c.output }}</pre>
              </details>
            </div>
          </div>
        </div>
        <div v-if="checksByRevision.length === 0" class="panel p-10 text-center text-xs" style="color: var(--muted)">No verification evidence yet.</div>
      </div>

      <!-- CHANGES & PR -->
      <div v-else key="ch" class="mt-4 grid gap-4 lg:grid-cols-2">
        <div class="panel p-5">
          <div class="eyebrow mb-3">Builder changes (harness output)</div>
          <div v-if="builderRuns.length === 0" class="text-xs" style="color: var(--muted)">No implementation runs yet.</div>
          <details v-for="r in builderRuns" :key="r.id" class="mb-2 rounded-xl p-3" style="background: var(--panel2)" :open="builderRuns.length === 1">
            <summary class="cursor-pointer text-xs font-bold">
              {{ r.task_id ? `task ${r.task_id}` : 'remediation run' }} · {{ r.harness }} · {{ r.state }}
            </summary>
            <pre class="mono mt-2 whitespace-pre-wrap text-[11px]" style="color: var(--muted)">{{ r.output || r.error }}</pre>
          </details>
          <p class="mt-3 text-[11px]" style="color: var(--muted)">Nothing is published or merged by this MVP. The PR decision below records a local disposition only.</p>
        </div>
        <div class="panel p-5">
          <div class="eyebrow mb-3">Findings</div>
          <div v-if="factory.findings.length === 0" class="text-xs" style="color: var(--muted)">No findings recorded.</div>
          <div v-for="f in factory.findings" :key="f.id" class="mb-2 rounded-xl p-3" style="background: var(--panel2)">
            <div class="flex items-center gap-2">
              <span class="badge" :class="f.severity === 'blocker' ? 'red' : 'amber'">{{ f.severity }}</span>
              <span class="text-sm font-bold">{{ f.title }}</span>
              <span class="badge ml-auto">{{ f.status }}</span>
            </div>
            <p v-if="f.detail" class="mt-1 text-xs" style="color: var(--muted)">{{ f.detail }}</p>
            <div v-if="f.location" class="mono text-[10px]" style="color: var(--muted)">{{ f.location }}</div>
          </div>
          <div class="mt-3 flex flex-wrap gap-2">
            <button class="btn small primary" :disabled="busy || wo.state !== 'NEEDS_HUMAN_REVIEW'" @click="prApprove">Approve</button>
            <button class="btn small" :disabled="busy || wo.state !== 'NEEDS_HUMAN_REVIEW'" @click="prRequestChanges">Request changes</button>
            <button class="btn small danger" :disabled="busy || (wo.state !== 'NEEDS_HUMAN_REVIEW' && wo.state !== 'BLOCKED')" @click="prDecline">Decline</button>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>
