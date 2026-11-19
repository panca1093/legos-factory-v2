<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { ChevronRight, File, Folder } from "lucide-vue-next";
import { ROW_HEIGHT, visibleWindow, type TreeRow } from "./tree";

const props = defineProps<{
  rows: TreeRow[];
  selected?: string;
  height?: number;
}>();
const emit = defineEmits<{
  toggle: [row: TreeRow];
  select: [row: TreeRow, pin: boolean];
  menu: [row: TreeRow, ev: MouseEvent];
}>();
defineSlots<{
  marker?: (p: { row: TreeRow }) => unknown;
  badge?: (p: { row: TreeRow }) => unknown;
}>();

const box = ref<HTMLElement>();
const scrollTop = ref(0);
const viewport = ref(props.height ?? 320);
let ro: ResizeObserver | undefined;
onMounted(() => {
  if (box.value && typeof ResizeObserver !== "undefined") {
    ro = new ResizeObserver(() => {
      viewport.value = box.value?.clientHeight || viewport.value;
    });
    ro.observe(box.value);
  }
});
onBeforeUnmount(() => ro?.disconnect());

const win = computed(() =>
  visibleWindow(props.rows.length, scrollTop.value, viewport.value),
);
const shown = computed(() => props.rows.slice(win.value.start, win.value.end));

function click(row: TreeRow, ev: MouseEvent) {
  if (row.kind === "dir") emit("toggle", row);
  else emit("select", row, ev.detail > 1);
}
function key(row: TreeRow, ev: KeyboardEvent) {
  if (ev.key === "Enter" || ev.key === " ") {
    ev.preventDefault();
    if (row.kind === "dir") emit("toggle", row);
    else emit("select", row, false);
  }
  if (ev.key === "ArrowRight" && row.kind === "dir" && !row.open)
    emit("toggle", row);
  if (ev.key === "ArrowLeft" && row.kind === "dir" && row.open)
    emit("toggle", row);
}
</script>

<template>
  <div
    ref="box"
    class="ft"
    role="tree"
    :style="{ height: height ? `${height}px` : undefined }"
    data-test="file-tree"
    @scroll.passive="scrollTop = ($event.target as HTMLElement).scrollTop"
  >
    <div class="ft-space" :style="{ height: `${rows.length * ROW_HEIGHT}px` }">
      <div
        v-for="(row, i) in shown"
        :key="row.path"
        class="ft-row"
        :class="{
          sel: row.path === selected,
          ignored: row.ignored,
          dir: row.kind === 'dir',
        }"
        :style="{
          top: `${(win.start + i) * ROW_HEIGHT}px`,
          paddingLeft: `${8 + row.depth * 14}px`,
        }"
        role="treeitem"
        :aria-expanded="row.kind === 'dir' ? row.open : undefined"
        :aria-level="row.depth + 1"
        tabindex="0"
        :title="row.path"
        data-test="tree-row"
        @click="click(row, $event)"
        @keydown="key(row, $event)"
        @contextmenu.prevent="emit('menu', row, $event)"
      >
        <ChevronRight
          v-if="row.kind === 'dir'"
          class="icon ft-chev"
          :class="{ open: row.open }"
        />
        <span v-else class="ft-chev"></span>
        <Folder v-if="row.kind === 'dir'" class="icon ft-ico" />
        <File v-else class="icon ft-ico" />
        <span class="ft-name">{{ row.label }}</span>
        <span class="ft-end">
          <slot name="badge" :row="row" />
          <slot name="marker" :row="row" />
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ft {
  position: relative;
  overflow-y: auto;
  overflow-x: hidden;
  min-height: 0;
}
.ft-space {
  position: relative;
}
.ft-row {
  position: absolute;
  left: 0;
  right: 0;
  height: 24px;
  display: flex;
  align-items: center;
  gap: 4px;
  padding-right: 8px;
  border-radius: var(--radius-xs);
  font-size: 13px;
  color: var(--ink);
  cursor: pointer;
  white-space: nowrap;
}
.ft-row:hover {
  background: var(--surface-2);
}
.ft-row:focus-visible {
  outline: none;
  box-shadow: var(--shadow-focus);
}
.ft-row.sel {
  background: var(--tint);
  font-weight: 600;
}
.ft-row.ignored {
  opacity: 0.55;
}
.ft-chev {
  flex: none;
  width: 12px;
  height: 12px;
  color: var(--muted);
  transition: transform var(--dur-fast);
}
.ft-chev.open {
  transform: rotate(90deg);
}
.ft-ico {
  flex: none;
  width: 14px;
  height: 14px;
  color: var(--muted);
}
.ft-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ft-end {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
</style>
