<script setup lang="ts">
import { onBeforeUnmount, onMounted, shallowRef } from "vue";
import type { ITextModel } from "./monaco";
import { useEditor } from "./editorStore";

const props = defineProps<{
  disk: string;
  model: ITextModel;
  language: string;
}>();
const store = useEditor();
const el = shallowRef<HTMLElement>();
let diff: { dispose(): void } | null = null;
let original: ITextModel | null = null;

onMounted(() => {
  const monaco = store.monaco;
  if (!monaco || !el.value) return;
  original = monaco.editor.createModel(props.disk, props.language);
  const d = monaco.editor.createDiffEditor(el.value, {
    automaticLayout: true,
    originalEditable: false,
    renderSideBySide: true,
    minimap: { enabled: false },
    theme: "legos",
  });
  d.setModel({ original, modified: props.model });
  diff = d;
});

onBeforeUnmount(() => {
  diff?.dispose();
  original?.dispose();
  diff = original = null;
});
</script>

<template>
  <div ref="el" class="md" data-test="monaco-diff"></div>
</template>

<style scoped>
.md {
  width: 100%;
  height: 100%;
}
</style>
