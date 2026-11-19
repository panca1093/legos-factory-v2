<script setup lang="ts">
import { computed } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { X } from "lucide-vue-next";
import { fileMarker } from "../../components/files/tree";
import { useEditor, type FolderState } from "./editorStore";

const props = defineProps<{ projectId: string }>();
const route = useRoute(),
  store = useEditor();

const groups = computed(() =>
  Object.entries(store.folders)
    .filter(
      ([, s]) =>
        s.status !== "gone" &&
        s.tabs.length &&
        (s.folder.project_id || store.workspaceProject) === props.projectId,
    )
    .sort(
      ([, a], [, b]) =>
        Number(a.folder.kind !== "project") -
        Number(b.folder.kind !== "project"),
    )
    .map(([fid, s]) => ({ fid, s })),
);
const count = computed(() =>
  groups.value.reduce((n, g) => n + g.s.tabs.length, 0),
);
defineExpose({ count });

const base = (g: { fid: string; s: FolderState }) =>
  g.s.folder.kind === "project"
    ? `/projects/${props.projectId}/code`
    : `/projects/${props.projectId}/code/${g.fid}`;
const to = (g: { fid: string; s: FolderState }, path: string) => ({
  path: base(g),
  query: { file: path },
});
const selected = (g: { fid: string; s: FolderState }, path: string) =>
  route.name === "project-code" &&
  String(route.params.folder ?? "") ===
    (g.s.folder.kind === "project" ? "" : g.fid) &&
  g.s.active === path;
const marker = (g: { s: FolderState }, path: string) =>
  g.s.markers ? fileMarker(g.s.markers, path) : undefined;
const label = (g: { s: FolderState }) =>
  `${g.s.folder.wo_id || "worktree"} worktree`;
</script>

<template>
  <div class="rof" data-test="rail-open-files">
    <p v-if="!count" class="pr-muted">No files open</p>
    <template v-for="g in groups" :key="g.fid">
      <p v-if="g.s.folder.kind !== 'project'" class="rof-label mono">
        {{ label(g) }}
      </p>
      <div v-for="t in g.s.tabs" :key="t.path" class="rof-row">
        <RouterLink
          :to="to(g, t.path)"
          class="pr-row"
          :class="{ active: selected(g, t.path) }"
          :title="t.path"
          data-test="rail-open-file"
          ><span
            class="rof-dot"
            :class="{ on: g.s.buffers[t.path]?.dirty }"
            aria-hidden="true"
          ></span
          ><span class="t" :class="{ it: t.preview }">{{
            t.path.split("/").pop()
          }}</span
          ><span v-if="marker(g, t.path)" class="rof-mk mono">{{
            marker(g, t.path)
          }}</span
          ><span v-if="g.s.buffers[t.path]?.dirty" class="sr-only"
            >unsaved</span
          ></RouterLink
        >
        <button
          type="button"
          class="rof-x"
          :aria-label="`Close ${t.path.split('/').pop()}`"
          @click="store.requestClose(g.fid, t.path)"
        >
          <X class="icon" />
        </button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.rof {
  display: grid;
  gap: 2px;
}
.rof-row {
  position: relative;
  display: flex;
  align-items: center;
}
.rof-row .pr-row {
  flex: 1;
  min-width: 0;
}
.rof-label {
  margin: 6px 0 0;
  padding: 0 12px;
  font-size: 11px;
  color: var(--muted);
}
.rof-dot {
  flex: none;
  width: 7px;
  height: 7px;
  border-radius: 50%;
}
.rof-dot.on {
  background: var(--warning);
}
.it {
  font-style: italic;
}
.rof-mk {
  margin-left: auto;
  color: var(--warning);
  font-size: 11px;
  font-weight: 700;
}
.rof-x {
  position: absolute;
  right: 4px;
  display: none;
  place-items: center;
  width: 20px;
  height: 20px;
  border: 0;
  border-radius: var(--radius-xs);
  background: var(--surface-2);
  color: var(--muted);
  cursor: pointer;
}
.rof-row:hover .rof-x,
.rof-x:focus-visible {
  display: grid;
}
.rof-x .icon {
  width: 12px;
  height: 12px;
}
</style>
