<script setup lang="ts">
import { computed, defineAsyncComponent, type Component } from "vue";
import ExplorerCard from "./ExplorerCard.vue";
import { useEditor } from "./editorStore";

const store = useEditor();

// Problems and Language server cards belong to the language client; they mount when their files exist.
const found = import.meta.glob<{ default: Component }>([
  "./ProblemsCard.vue",
  "./ServerCard.vue",
]);
const problems = found["./ProblemsCard.vue"]
  ? defineAsyncComponent(found["./ProblemsCard.vue"])
  : null;
const server = found["./ServerCard.vue"]
  ? defineAsyncComponent(found["./ServerCard.vue"])
  : null;

const st = computed(() => store.current());
const language = computed(() => {
  const s = st.value;
  return s?.active ? (s.buffers[s.active]?.language ?? null) : null;
});
const folderPath = computed(() => st.value?.folder.path);
function open(e: { uri: string; line: number; column: number }) {
  void store.openFileAt(e.uri, { line: e.line, column: e.column });
}
</script>

<template>
  <ExplorerCard />
  <component
    :is="problems"
    v-if="problems"
    :diagnostics="store.diagnostics"
    :folder-path="folderPath"
    @open="open"
  />
  <component :is="server" v-if="server" :language="language" />
</template>
