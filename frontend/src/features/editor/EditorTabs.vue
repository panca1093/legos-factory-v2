<script setup lang="ts">
import { X } from "lucide-vue-next";
import { useEditor } from "./editorStore";

const props = defineProps<{ folderId: string }>();
const store = useEditor();

const tabs = () => store.folders[props.folderId]?.tabs ?? [];
const active = () => store.folders[props.folderId]?.active ?? "";
const dirty = (p: string) => !!store.folders[props.folderId]?.buffers[p]?.dirty;
const name = (p: string) => p.split("/").pop() ?? p;
function aux(e: MouseEvent, p: string) {
  if (e.button === 1) {
    e.preventDefault();
    store.requestClose(props.folderId, p);
  }
}
</script>

<template>
  <div
    class="et"
    role="tablist"
    aria-label="Open files"
    data-test="editor-tabs"
  >
    <div
      v-for="t in tabs()"
      :key="t.path"
      class="et-tab"
      :class="{ on: t.path === active(), preview: t.preview }"
      role="tab"
      :aria-selected="t.path === active()"
      :title="t.path"
      data-test="editor-tab"
      @click="store.setActive(folderId, t.path)"
      @dblclick="store.pinTab(folderId, t.path)"
      @auxclick="aux($event, t.path)"
    >
      <span class="mono et-name">{{ name(t.path) }}</span>
      <span
        v-if="dirty(t.path)"
        class="et-dot"
        title="Unsaved changes"
        aria-label="Unsaved changes"
      ></span>
      <button
        type="button"
        class="et-x"
        :aria-label="`Close ${name(t.path)}`"
        @click.stop="store.requestClose(folderId, t.path)"
      >
        <X class="icon" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.et {
  display: flex;
  gap: 2px;
  padding: 6px 8px 0;
  background: var(--surface-2);
  border-bottom: 1px solid var(--line);
  overflow-x: auto;
}
.et-tab {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  height: 34px;
  padding: 0 8px 0 14px;
  border-radius: 10px 10px 0 0;
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
}
.et-tab.on {
  background: var(--surface);
  color: var(--ink);
  font-weight: 600;
  box-shadow: 0 -1px 0 var(--line) inset;
}
.et-tab.preview .et-name {
  font-style: italic;
}
.et-name {
  font-size: 12.5px;
}
.et-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--warning);
}
.et-x {
  display: grid;
  place-items: center;
  width: 20px;
  height: 20px;
  border: 0;
  border-radius: var(--radius-xs);
  background: none;
  color: var(--muted);
  opacity: 0;
  cursor: pointer;
}
.et-tab:hover .et-x,
.et-tab.on .et-x,
.et-x:focus-visible {
  opacity: 1;
}
.et-x:hover {
  background: var(--line);
  color: var(--ink);
}
.et-x .icon {
  width: 12px;
  height: 12px;
}
</style>
