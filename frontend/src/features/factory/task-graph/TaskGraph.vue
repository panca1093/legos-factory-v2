<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import {
  ZoomIn,
  ZoomOut,
  Maximize2,
  GitBranch,
  Crosshair,
} from "lucide-vue-next";
import type { Task, Run } from "../../../types/api";
import {
  layoutTasks,
  lineage,
  type Direction,
  type GraphEdge,
  type GraphLayout,
  type LayoutEngine,
} from "./layout";
import { loadElk } from "./elk";
import { persona, taskState } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
import AppDialog from "../../../components/ui/AppDialog.vue";
const props = withDefaults(
  defineProps<{
    tasks: Task[];
    runs: Run[];
    engine?: () => Promise<LayoutEngine>;
  }>(),
  { engine: loadElk },
);
const emit = defineEmits<{ select: [string] }>();
const markers = {
  "": "var(--muted)",
  "-active": "var(--violet)",
  "-cycle": "var(--red)",
};
const MIN_ZOOM = 0.2,
  MAX_ZOOM = 2,
  READABLE_ZOOM = 0.5;
const direction = ref<Direction>(
    window.matchMedia?.("(max-width: 640px)").matches ? "DOWN" : "RIGHT",
  ),
  graph = ref<GraphLayout | null>(null),
  selectedID = ref<string | null>(null),
  inspectID = ref<string | null>(null),
  focus = ref(false),
  zoom = ref(1),
  panX = ref(0),
  panY = ref(0),
  viewport = ref<HTMLElement>(),
  marker = useId();
const ordered = computed(() =>
  [...props.tasks].sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0)),
);
// Only structure changes trigger a relayout; run updates just restyle cards.
const structure = computed(() =>
  ordered.value.map((t) => `${t.id}<${(t.deps ?? []).join(",")}`).join("|"),
);
let request = 0;
watch(
  [structure, direction],
  async () => {
    const current = ++request;
    const result = await layoutTasks(
      ordered.value,
      direction.value,
      props.engine,
    );
    if (current !== request) return;
    graph.value = result;
    await nextTick();
    fit(READABLE_ZOOM);
  },
  { immediate: true },
);
const diagnostics = computed(() => graph.value?.diagnostics ?? []);
const states = computed(
  () => new Map(props.tasks.map((t) => [t.id, taskState(t.id, props.runs)])),
);
const counts = computed(() => {
  const all = [...states.value.values()];
  return {
    completed: all.filter((s) => s === "completed").length,
    running: all.filter((s) => s === "running").length,
    failed: all.filter((s) => s === "failed").length,
    waiting: all.filter((s) => !["completed", "running", "failed"].includes(s))
      .length,
  };
});
const progress = computed(() =>
  props.tasks.length
    ? Math.round((counts.value.completed / props.tasks.length) * 100)
    : 0,
);
const focused = computed(() =>
  focus.value && selectedID.value && graph.value
    ? lineage(selectedID.value, graph.value.edges)
    : null,
);
const selected = computed(() =>
  props.tasks.find((t) => t.id === inspectID.value),
);
const selectedRuns = computed(() =>
  props.runs.filter((r) => r.task_id === inspectID.value),
);
watch(selectedID, (id) => {
  if (!id) focus.value = false;
});

const statusInfo: Record<string, { label: string; tone: string }> = {
  completed: { label: "Completed", tone: "green" },
  running: { label: "In progress", tone: "purple" },
  failed: { label: "Failed", tone: "red" },
  waiting: { label: "Waiting", tone: "" },
};
function status(id: string) {
  const state = states.value.get(id) ?? "waiting";
  return (
    statusInfo[state] ?? {
      label: state.charAt(0).toUpperCase() + state.slice(1),
      tone: "",
    }
  );
}
function duration(ms: number) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}
function meta(id: string) {
  const state = states.value.get(id);
  if (state === "running") return "Running";
  if (state === "failed") return "Stopped";
  if (state !== "completed") return "Pending";
  const run = props.runs.filter((r) => r.task_id === id).at(-1);
  return run?.finished_at
    ? duration(Date.parse(run.finished_at) - Date.parse(run.created_at))
    : "Done";
}
const roleName = (role: string) =>
  role ? role.charAt(0).toUpperCase() + role.slice(1) : "Task";
const sequence = (t: Task, i: number) =>
  `#${String(t.seq || i + 1).padStart(2, "0")}`;
function edgeClass(e: GraphEdge) {
  if (focused.value) {
    const on = focused.value.has(e.from) && focused.value.has(e.to);
    return { active: on, dim: !on, cycle: e.cycle };
  }
  return {
    active: !!selectedID.value && [e.from, e.to].includes(selectedID.value),
    cycle: e.cycle,
  };
}
const arrow = (e: GraphEdge) =>
  `url(#${marker}${edgeClass(e).active ? "-active" : e.cycle ? "-cycle" : ""})`;
const hasCycle = computed(() => !!graph.value?.edges.some((e) => e.cycle));

function select(id: string) {
  if (dragged) return void (dragged = false);
  selectedID.value = id;
  inspectID.value = id;
  emit("select", id);
}
// After a drag the click lands on the captured canvas, so it resets the flag.
function clearSelection(e: MouseEvent) {
  const wasDrag = dragged;
  dragged = false;
  if (wasDrag || (e.target as HTMLElement).closest("button")) return;
  selectedID.value = null;
}

function zoomTo(next: number, cx?: number, cy?: number) {
  const el = viewport.value;
  next = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, next));
  const x = cx ?? (el?.clientWidth ?? 0) / 2,
    y = cy ?? (el?.clientHeight ?? 0) / 2;
  panX.value = x - ((x - panX.value) / zoom.value) * next;
  panY.value = y - ((y - panY.value) / zoom.value) * next;
  zoom.value = next;
}
// The initial fit keeps cards legible; the fit button shows the whole graph.
function fit(minimum = MIN_ZOOM) {
  const el = viewport.value,
    g = graph.value;
  if (!el || !g || !g.width || !el.clientWidth) {
    zoom.value = 1;
    panX.value = panY.value = 0;
    return;
  }
  const w = el.clientWidth,
    h = el.clientHeight - 36,
    scale = Math.min(1, Math.max(minimum, Math.min(w / g.width, h / g.height)));
  zoom.value = scale;
  panX.value = Math.max(0, (w - g.width * scale) / 2);
  panY.value = Math.max(0, (h - g.height * scale) / 2);
}
function onWheel(e: WheelEvent) {
  const rect = viewport.value!.getBoundingClientRect();
  zoomTo(
    zoom.value * Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015)),
    e.clientX - rect.left,
    e.clientY - rect.top,
  );
}
function onKey(e: KeyboardEvent) {
  if (e.target !== viewport.value) return;
  const step = 48,
    moves: Record<string, [number, number]> = {
      ArrowLeft: [step, 0],
      ArrowRight: [-step, 0],
      ArrowUp: [0, step],
      ArrowDown: [0, -step],
    };
  if (e.key === "+" || e.key === "=") zoomTo(zoom.value * 1.2);
  else if (e.key === "-") zoomTo(zoom.value / 1.2);
  else if (e.key === "0") fit();
  else if (moves[e.key]) {
    panX.value += moves[e.key][0];
    panY.value += moves[e.key][1];
  } else return;
  e.preventDefault();
}
let pan: { x: number; y: number; left: number; top: number } | null = null,
  panning = false,
  dragged = false;
function startPan(e: PointerEvent) {
  if (e.button !== 0) return;
  dragged = panning = false;
  pan = { x: e.clientX, y: e.clientY, left: panX.value, top: panY.value };
}
function movePan(e: PointerEvent) {
  if (!pan || !viewport.value) return;
  const dx = e.clientX - pan.x,
    dy = e.clientY - pan.y;
  if (!panning && Math.hypot(dx, dy) < 4) return;
  if (!panning) {
    panning = dragged = true;
    viewport.value.setPointerCapture?.(e.pointerId);
  }
  panX.value = pan.left + dx;
  panY.value = pan.top + dy;
}
function endPan() {
  pan = null;
  panning = false;
}
</script>
<template>
  <section class="tg">
    <div class="graph-toolbar">
      <div>
        <h2>The blueprint, in motion.</h2>
        <p class="help">
          Dependencies point forward. Select a task to inspect its contract.
        </p>
      </div>
      <div class="tg-controls">
        <div class="tg-segment" role="group" aria-label="Layout direction">
          <button
            :aria-pressed="direction === 'RIGHT'"
            @click="direction = 'RIGHT'"
          >
            Left → right
          </button>
          <button
            :aria-pressed="direction === 'DOWN'"
            @click="direction = 'DOWN'"
          >
            Top ↓ down
          </button>
        </div>
        <button
          class="btn tg-focus"
          :class="{ on: focus }"
          :aria-pressed="focus"
          :disabled="!selectedID"
          :title="
            selectedID ? 'Show only this task’s path' : 'Select a task first'
          "
          @click="focus = !focus"
        >
          <Crosshair class="icon" />Focus path
        </button>
      </div>
    </div>
    <div
      v-for="diagnostic in diagnostics"
      :key="diagnostic"
      class="notice"
      role="alert"
      style="margin-bottom: 12px"
    >
      {{ diagnostic }}
    </div>
    <div v-if="!tasks.length" class="empty">
      The task graph will appear after the blueprint is drafted.
    </div>
    <template v-else>
      <div class="tg-summary">
        <div
          class="tg-progress"
          role="progressbar"
          aria-label="Tasks completed"
          :aria-valuenow="progress"
          aria-valuemin="0"
          aria-valuemax="100"
        >
          <span :style="{ width: progress + '%' }"></span>
        </div>
        <p class="tiny muted">
          <strong
            >{{ counts.completed }} of {{ tasks.length }} completed</strong
          >
          · <i class="tg-dot running"></i>{{ counts.running }} in progress
          <template v-if="counts.failed">
            · <i class="tg-dot failed"></i>{{ counts.failed }} failed
          </template>
          · <i class="tg-dot waiting"></i>{{ counts.waiting }} queued
        </p>
      </div>
      <div
        ref="viewport"
        class="tg-canvas"
        :class="{ focusing: !!focused }"
        aria-label="Task dependency graph"
        tabindex="0"
        @pointerdown="startPan"
        @pointermove="movePan"
        @pointerup="endPan"
        @pointercancel="endPan"
        @lostpointercapture="endPan"
        @wheel.prevent="onWheel"
        @keydown="onKey"
        @click="clearSelection"
      >
        <div
          v-if="graph"
          class="tg-world"
          :style="{
            width: graph.width + 'px',
            height: graph.height + 'px',
            transform: `translate(${panX}px, ${panY}px) scale(${zoom})`,
          }"
        >
          <svg :width="graph.width" :height="graph.height" aria-hidden="true">
            <defs>
              <marker
                v-for="(fill, kind) in markers"
                :id="marker + kind"
                :key="kind"
                markerWidth="8"
                markerHeight="6"
                refX="7"
                refY="3"
                orient="auto"
                markerUnits="userSpaceOnUse"
              >
                <path d="M0,0 L8,3 L0,6 Z" :fill="fill" />
              </marker>
            </defs>
            <text
              v-for="rank in graph.ranks"
              :key="rank.label"
              class="tg-rank"
              :x="rank.x"
              :y="rank.y"
              :dominant-baseline="direction === 'DOWN' ? 'middle' : undefined"
            >
              {{ rank.label }}
            </text>
            <path
              v-for="e in graph.edges"
              :key="e.id"
              class="tg-edge"
              :class="edgeClass(e)"
              :d="e.path"
              :marker-end="arrow(e)"
            /></svg
          ><button
            v-for="(n, i) in graph.nodes"
            :key="n.id"
            class="task-brick tg-node"
            :class="[
              states.get(n.id) ?? 'waiting',
              {
                selected: selectedID === n.id,
                dim: focused && !focused.has(n.id),
              },
            ]"
            :data-node="n.id"
            :style="{
              left: n.x + 'px',
              top: n.y + 'px',
              width: n.width + 'px',
              height: n.height + 'px',
            }"
            :aria-label="`${n.title}: ${status(n.id).label}`"
            @click="select(n.id)"
          >
            <span class="tg-node-head"
              ><BrickBot :persona="persona(n.role)" :size="16" /><span
                class="tg-role"
                >{{ roleName(n.role) }}</span
              ><span class="grow"></span
              ><span
                v-if="n.dependents"
                class="tg-badge"
                :title="`${n.dependents} ${n.dependents === 1 ? 'task depends' : 'tasks depend'} on this one`"
                >Needed by {{ n.dependents }}</span
              ><span class="tg-seq">{{ sequence(n, i) }}</span></span
            ><strong class="tg-node-title">{{ n.title }}</strong
            ><span class="tg-node-foot"
              ><span class="pill" :class="status(n.id).tone">{{
                status(n.id).label
              }}</span
              ><span class="tiny muted">{{ meta(n.id) }}</span></span
            >
          </button>
        </div>
        <p v-else class="tg-loading tiny muted">Arranging tasks…</p>
        <span class="tg-engine">ELK · Layered / Orthogonal</span>
        <div class="tg-zoom" @pointerdown.stop>
          <button aria-label="Zoom in" @click="zoomTo(zoom * 1.2)">
            <ZoomIn class="icon" />
          </button>
          <button aria-label="Zoom out" @click="zoomTo(zoom / 1.2)">
            <ZoomOut class="icon" />
          </button>
          <button aria-label="Fit graph" @click="fit()">
            <Maximize2 class="icon" />
          </button>
        </div>
        <p class="tg-hint">Drag to pan · Scroll to zoom · Click to inspect</p>
      </div>
      <div class="tg-footer tiny muted">
        <span class="row"
          ><GitBranch class="icon" />{{ tasks.length }} tasks ·
          {{ graph?.edges.length ?? 0 }} dependencies ·
          {{ Math.round(zoom * 100) }}% zoom</span
        >
        <span class="tg-legend"
          ><span class="tg-eyebrow">Legend</span
          ><span><i class="tg-dot completed"></i>Done</span
          ><span><i class="tg-dot running"></i>Running</span
          ><span><i class="tg-dot waiting"></i>Waiting</span
          ><span
            ><b class="tg-badge">Needed by N</b>Tasks that depend on it</span
          ><span v-if="hasCycle"><i class="tg-line-cycle"></i>Cycle</span></span
        >
      </div>
    </template>
  </section>
  <AppDialog
    :open="!!selected"
    :title="selected?.title ?? 'Task details'"
    @close="inspectID = null"
    ><div v-if="selected" class="stack">
      <div class="row">
        <BrickBot :persona="persona(selected.role)" class="medium" />
        <div>
          <strong>{{ selected.role }}</strong>
          <p class="help">{{ taskState(selected.id, runs) }}</p>
        </div>
      </div>
      <div>
        <h3>Task contract</h3>
        <p class="small muted" style="margin-top: 9px; white-space: pre-wrap">
          {{ selected.objective }}
        </p>
      </div>
      <div>
        <h3>Dependencies</h3>
        <ul v-if="selected.deps?.length" class="check-list">
          <li v-for="d in selected.deps" :key="d">
            {{ tasks.find((t) => t.id === d)?.title ?? d }}
          </li>
        </ul>
        <p v-else class="help">
          No dependencies. This task can start when authorized.
        </p>
      </div>
      <div v-for="run in selectedRuns" :key="run.id">
        <span class="pill">{{ run.harness }} · {{ run.state }}</span>
        <pre v-if="run.output || run.error" class="output">{{
          run.error || run.output
        }}</pre>
      </div>
    </div></AppDialog
  >
</template>
