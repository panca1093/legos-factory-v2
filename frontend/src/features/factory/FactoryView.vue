<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useFactory } from '../../stores/factory'
import { Plus, X, Search, Boxes } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import StatusBadge from '../../components/ui/StatusBadge.vue'

const factory = useFactory()
const router = useRouter()
const search = ref('')
const view = ref<'board' | 'list'>('board')

const columns = [
  { key: 'plan', label: 'Planning', states: ['CREATED', 'REQUIREMENT_ANALYSIS', 'TASK_DECOMPOSITION', 'SPEC_AND_PLAN_GENERATION', 'ARCHITECT_REVIEW', 'PLANNING'] },
  { key: 'human', label: 'Needs human', states: ['WAITING_FOR_HUMAN'] },
  { key: 'build', label: 'Building', states: ['QUEUED', 'IMPLEMENTING', 'VERIFYING', 'SPECIALIST_REVIEW'] },
  { key: 'done', label: 'Ready / done', states: ['PR_PREPARATION', 'MERGE_READINESS', 'READY', 'NEEDS_HUMAN_REVIEW', 'BLOCKED', 'CANCELLED'] },
] as const

const filtered = computed(() =>
  factory.workOrders.filter(
    (w) =>
      w.title.toLowerCase().includes(search.value.toLowerCase()) ||
      w.objective.toLowerCase().includes(search.value.toLowerCase()),
  ),
)

function byColumn(states: readonly string[]) {
  return filtered.value.filter((w) => states.includes(w.state))
}

onMounted(async () => {
  await Promise.all([factory.refreshProjects(), factory.refreshWorkOrders()])
})

// drawer state
const showNew = ref(false)
const form = ref({
  project_id: '', title: '', objective: '', acceptance: '', autonomy: 'assisted',
  builder_model: '', inspector_model: '',
})
const creating = ref(false)
const formError = ref('')

function openNew() {
  form.value = { project_id: factory.projects[0]?.id ?? '', title: '', objective: '', acceptance: '', autonomy: 'assisted', builder_model: '', inspector_model: '' }
  formError.value = ''
  showNew.value = true
}

async function create() {
  formError.value = ''
  if (!form.value.title.trim() || !form.value.objective.trim() || !form.value.project_id) {
    formError.value = 'Title, objective and project are required.'
    return
  }
  creating.value = true
  try {
    const wo = await factory.createWorkOrder(form.value)
    showNew.value = false
    // draft the blueprint immediately (Foreman planning)
    await factory.planGenerate(wo.id)
    router.push(`/factory/${wo.id}`)
  } catch (e) {
    formError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

const selectedProject = computed(() => factory.projects.find((p) => p.id === form.value.project_id))

// new project form
const showNewProject = ref(false)
const projForm = ref({ name: '', repo_url: '', language: 'go' })
const projError = ref('')

async function createProject() {
  projError.value = ''
  if (!projForm.value.name.trim()) {
    projError.value = 'Name is required.'
    return
  }
  try {
    await factory.createProject(projForm.value)
    showNewProject.value = false
  } catch (e) {
    projError.value = e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <div>
    <div class="mb-5 flex flex-wrap items-center gap-3">
      <h1 class="text-2xl font-extrabold">Factory</h1>
      <div class="relative ml-auto">
        <Search :size="15" class="absolute left-3 top-1/2 -translate-y-1/2" style="color: var(--muted)" />
        <input v-model="search" class="input pl-8" style="width: 240px" placeholder="Search work orders…" aria-label="Search work orders" />
      </div>
      <div class="flex overflow-hidden rounded-xl border" style="border-color: var(--line)" role="tablist" aria-label="View">
        <button v-for="v in ['board', 'list']" :key="v" class="px-3 py-1.5 text-xs font-bold"
          :style="view === v ? 'background: var(--violet-soft); color: var(--violet)' : ''"
          role="tab" :aria-selected="view === v" @click="view = v">{{ v }}</button>
      </div>
      <button class="btn" @click="showNewProject = true"><Boxes :size="15" /> New project</button>
      <button class="btn primary" @click="openNew"><Plus :size="15" /> New work order</button>
    </div>

    <!-- board -->
    <div v-if="view === 'board'" class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
      <div v-for="col in columns" :key="col.key" class="rounded-2xl p-3" style="background: var(--panel2)">
        <div class="mb-2 flex items-center justify-between px-1">
          <div class="eyebrow">{{ col.label }}</div>
          <div class="badge">{{ byColumn(col.states).length }}</div>
        </div>
        <TransitionGroup name="brick" tag="div" class="flex flex-col gap-3">
          <RouterLink
            v-for="wo in byColumn(col.states)"
            :key="wo.id"
            :to="`/factory/${wo.id}`"
            class="panel cursor-pointer p-4 no-underline"
            style="color: var(--ink)"
          >
            <div class="mb-1 text-sm font-extrabold">{{ wo.title }}</div>
            <p class="line-clamp-2 text-xs" style="color: var(--muted)">{{ wo.objective }}</p>
            <div class="mt-3 flex items-center justify-between">
              <StatusBadge :state="wo.state" />
              <span class="text-[10px] mono" style="color: var(--muted)">rev {{ wo.revision }}</span>
            </div>
          </RouterLink>
        </TransitionGroup>
        <div v-if="byColumn(col.states).length === 0" class="py-6 text-center text-xs" style="color: var(--muted)">Empty</div>
      </div>
    </div>

    <!-- list -->
    <div v-else class="panel divide-y" style="border-color: var(--line)">
      <RouterLink v-for="wo in filtered" :key="wo.id" :to="`/factory/${wo.id}`"
        class="flex items-center gap-4 p-4 no-underline" style="color: var(--ink); border-color: var(--line)">
        <div class="flex-1 font-bold">{{ wo.title }}</div>
        <div class="hidden w-56 truncate text-xs md:block" style="color: var(--muted)">{{ wo.objective }}</div>
        <StatusBadge :state="wo.state" />
      </RouterLink>
      <div v-if="filtered.length === 0" class="p-10 text-center text-sm" style="color: var(--muted)">
        No work orders yet — create the first one.
      </div>
    </div>

    <!-- new work order drawer -->
    <Teleport to="body">
      <div v-if="showNew" class="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true" aria-label="New work order">
        <div class="absolute inset-0 bg-black/40" @click="showNew = false"></div>
        <Transition name="brick" appear>
          <div class="relative flex h-full w-full max-w-md flex-col overflow-y-auto p-6" style="background: var(--panel)">
            <div class="mb-4 flex items-center justify-between">
              <h2 class="text-lg font-extrabold">New work order</h2>
              <button class="icon-btn" aria-label="Close" @click="showNew = false"><X :size="18" /></button>
            </div>

            <div class="flex flex-col gap-4">
              <div>
                <label class="label" for="wo-project">Project</label>
                <select id="wo-project" v-model="form.project_id" class="input mt-1">
                  <option v-for="p in factory.projects" :key="p.id" :value="p.id">{{ p.name }}</option>
                </select>
                <div class="mt-1 text-[11px]" style="color: var(--muted)">Repository: {{ selectedProject?.repo_url || '(no repo configured)' }}</div>
              </div>
              <div>
                <label class="label" for="wo-title">Title</label>
                <input id="wo-title" v-model="form.title" class="input mt-1" placeholder="Make payment retries idempotent" />
              </div>
              <div>
                <label class="label" for="wo-objective">Desired outcome</label>
                <textarea id="wo-objective" v-model="form.objective" class="input mt-1" rows="3" placeholder="What should be true when this is done?"></textarea>
              </div>
              <div>
                <label class="label" for="wo-acceptance">Observable acceptance criteria</label>
                <textarea id="wo-acceptance" v-model="form.acceptance" class="input mt-1" rows="2" placeholder="Tests/commands that prove it"></textarea>
              </div>
              <div>
                <label class="label" for="wo-autonomy">Autonomy</label>
                <select id="wo-autonomy" v-model="form.autonomy" class="input mt-1">
                  <option value="guided">Guided — advance each step</option>
                  <option value="assisted">Assisted — runs after plan approval</option>
                  <option value="delegated">Delegated — proceed under policy</option>
                </select>
              </div>
              <div>
                <label class="label" for="wo-builder">Builder model</label>
                <select id="wo-builder" v-model="form.builder_model" class="input mt-1">
                  <option value="">Workspace default</option>
                  <option v-for="m in factory.models" :key="m.model_id" :value="`${m.provider_id}/${m.model_id}`">{{ m.name }}</option>
                </select>
              </div>
              <div>
                <label class="label" for="wo-inspector">Inspector model</label>
                <select id="wo-inspector" v-model="form.inspector_model" class="input mt-1">
                  <option value="">Workspace default</option>
                  <option v-for="m in factory.models" :key="m.model_id" :value="`${m.provider_id}/${m.model_id}`">{{ m.name }}</option>
                </select>
              </div>

              <p v-if="formError" class="text-xs font-bold" style="color: var(--red)" role="alert">{{ formError }}</p>

              <div class="mt-2 flex gap-2">
                <button class="btn primary flex-1 justify-center" :disabled="creating" @click="create">
                  {{ creating ? 'Creating…' : 'Create & draft blueprint' }}
                </button>
                <button class="btn" @click="showNew = false">Cancel</button>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Teleport>
    <!-- new project dialog -->
    <Teleport to="body">
      <div v-if="showNewProject" class="fixed inset-0 z-50 grid place-items-center p-4" role="dialog" aria-modal="true" aria-label="New project">
        <div class="absolute inset-0 bg-black/40" @click="showNewProject = false"></div>
        <Transition name="brick" appear>
          <div class="panel relative w-full max-w-md p-6">
            <div class="mb-3 flex items-center justify-between">
              <h2 class="text-lg font-extrabold">New project</h2>
              <button aria-label="Close" @click="showNewProject = false"><X :size="18" /></button>
            </div>
            <div class="flex flex-col gap-3">
              <div>
                <label class="label" for="prj-name">Name</label>
                <input id="prj-name" v-model="projForm.name" class="input mt-1" placeholder="Payments platform" />
              </div>
              <div>
                <label class="label" for="prj-repo">Repository (local path or URL)</label>
                <input id="prj-repo" v-model="projForm.repo_url" class="input mt-1" placeholder="/path/to/repo or https://…" />
              </div>
              <div>
                <label class="label" for="prj-lang">Language / quality profile</label>
                <select id="prj-lang" v-model="projForm.language" class="input mt-1">
                  <option value="go">Go — go test ./..., go vet</option>
                  <option value="typescript">TypeScript — typecheck, test</option>
                  <option value="rust">Rust — cargo check, cargo test</option>
                  <option value="node">Node — npm test</option>
                </select>
              </div>
              <p v-if="projError" class="text-xs font-bold" style="color: var(--red)" role="alert">{{ projError }}</p>
              <button class="btn primary justify-center" @click="createProject">Create project</button>
            </div>
          </div>
        </Transition>
      </div>
    </Teleport>
  </div>
</template>
