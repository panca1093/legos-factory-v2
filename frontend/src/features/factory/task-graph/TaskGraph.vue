<script setup lang="ts">
import { computed, ref, useId } from "vue";
import { ZoomIn, ZoomOut, Maximize2, GitBranch } from "lucide-vue-next";
import type { Task, Run } from "../../../types/api";
import { layoutTasks } from "./layout";
import { persona, taskState } from "../../../composables/presentation";
import BrickBot from "../../../components/agents/BrickBot.vue";
import AppDialog from "../../../components/ui/AppDialog.vue";
const props = defineProps<{ tasks: Task[]; runs: Run[] }>();
const emit = defineEmits<{ select: [string] }>();
const graph = computed(() => layoutTasks(props.tasks)),
  selectedID = ref<string | null>(null),
  zoom = ref(1),
  viewport = ref<HTMLElement>();
const selected = computed(() =>
    props.tasks.find((t) => t.id === selectedID.value),
  ),
  marker = useId();
const selectedRuns = computed(() =>
  props.runs.filter((r) => r.task_id === selectedID.value),
);
function select(id: string) {
  selectedID.value = id;
  emit("select", id);
}
function fit() {
  zoom.value = Math.max(
    0.35,
    Math.min(
      1,
      (viewport.value?.clientWidth ?? graph.value.width) / graph.value.width,
    ),
  );
}
function changeZoom(delta: number) {
  zoom.value = Math.max(
    0.35,
    Math.min(1.8, Math.round((zoom.value + delta) * 100) / 100),
  );
}
let pan: { x: number; y: number; left: number; top: number } | null = null;
function startPan(e: PointerEvent) {
  if ((e.target as HTMLElement).closest("button") || !viewport.value) return;
  pan = {
    x: e.clientX,
    y: e.clientY,
    left: viewport.value.scrollLeft,
    top: viewport.value.scrollTop,
  };
  viewport.value.setPointerCapture(e.pointerId);
}
function movePan(e: PointerEvent) {
  if (!pan || !viewport.value) return;
  viewport.value.scrollLeft = pan.left + pan.x - e.clientX;
  viewport.value.scrollTop = pan.top + pan.y - e.clientY;
}
</script>
<template>
  <section>
    <div class="graph-toolbar">
      <div>
        <h2>The blueprint, in motion.</h2>
        <p class="help">
          Dependencies point forward. Select a task to inspect its contract.
        </p>
      </div>
      <div class="row" style="gap: 6px">
        <button
          class="icon-btn"
          aria-label="Zoom out"
          @click="changeZoom(-0.15)"
        >
          <ZoomOut class="icon" /></button
        ><span class="tiny muted">{{ Math.round(zoom * 100) }}%</span
        ><button
          class="icon-btn"
          aria-label="Zoom in"
          @click="changeZoom(0.15)"
        >
          <ZoomIn class="icon" /></button
        ><button class="icon-btn" aria-label="Fit graph" @click="fit">
          <Maximize2 class="icon" />
        </button>
      </div>
    </div>
    <div
      v-for="diagnostic in graph.diagnostics"
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
    <div
      v-else
      ref="viewport"
      class="diagram-viewport graph-canvas"
      aria-label="Task dependency graph"
      tabindex="0"
      @pointerdown="startPan"
      @pointermove="movePan"
      @pointerup="pan = null"
      @pointercancel="pan = null"
      @lostpointercapture="pan = null"
    >
      <div
        :style="{
          width: graph.width * zoom + 'px',
          height: graph.height * zoom + 'px',
        }"
      >
        <div
          class="diagram-canvas"
          :style="{
            width: graph.width + 'px',
            height: graph.height + 'px',
            transform: `scale(${zoom})`,
          }"
        >
          <svg :width="graph.width" :height="graph.height" aria-hidden="true">
            <defs>
              <marker
                :id="marker"
                markerWidth="8"
                markerHeight="8"
                refX="7"
                refY="4"
                orient="auto"
              >
                <path d="M0,0 L8,4 L0,8 Z" fill="var(--muted)" />
              </marker>
            </defs>
            <path
              v-for="e in graph.edges"
              :key="e.from + '-' + e.to"
              :d="e.path"
              fill="none"
              stroke="var(--muted)"
              stroke-width="1.5"
              :marker-end="`url(#${marker})`"
            /></svg
          ><button
            v-for="n in graph.nodes"
            :key="n.id"
            class="task-brick node"
            :class="[taskState(n.id, runs), { selected: selectedID === n.id }]"
            :data-node="n.id"
            :style="{ left: n.x + 'px', top: n.y + 'px' }"
            :aria-label="`${n.title}: ${taskState(n.id, runs)}`"
            @click="select(n.id)"
          >
            <BrickBot :persona="persona(n.role)" class="compact" /><strong>{{
              n.title
            }}</strong
            ><span
              class="pill"
              :class="
                taskState(n.id, runs) === 'completed'
                  ? 'green'
                  : taskState(n.id, runs) === 'running'
                    ? 'purple'
                    : ''
              "
              >{{ taskState(n.id, runs) }}</span
            >
          </button>
        </div>
      </div>
    </div>
    <div class="row wrap tiny muted" style="margin-top: 16px">
      <GitBranch class="icon" />{{ tasks.length }} tasks ·
      {{ graph.edges.length }} dependencies<span class="grow"></span>Status
      comes from recorded task runs
    </div>
  </section>
  <AppDialog
    :open="!!selected"
    :title="selected?.title ?? 'Task details'"
    @close="selectedID = null"
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
