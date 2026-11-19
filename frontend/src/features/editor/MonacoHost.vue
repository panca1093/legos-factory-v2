<script setup lang="ts">
import { onBeforeUnmount, onMounted, shallowRef, watch } from "vue";
import type { ICodeEditor, ITextModel } from "./monaco";
import { useEditor } from "./editorStore";

const props = defineProps<{
  model: ITextModel | null;
  readOnly: boolean;
  reveal: {
    line: number;
    col: number;
    n: number;
    folderId?: string;
    path?: string;
  } | null;
  cursor?: { line: number; col: number };
}>();
const emit = defineEmits<{
  cursor: [line: number, col: number];
  save: [];
  saveAll: [];
  quick: [];
  closeTab: [];
  edited: [];
}>();

const store = useEditor();
const el = shallowRef<HTMLElement>();
let editor: ICodeEditor | null = null;
const views = new Map<string, unknown>();
let disposed = false;
let changeSub: { dispose(): void } | null = null;
let pending: NonNullable<typeof props.reveal> | null = null;

function attach(model: ITextModel | null, prev: ITextModel | null) {
  if (!editor) return;
  if (prev) views.set(prev.uri.toString(), editor.saveViewState());
  editor.setModel(model);
  changeSub?.dispose();
  changeSub = model ? model.onDidChangeContent(() => emit("edited")) : null;
  if (!model) return;
  const vs = views.get(model.uri.toString());
  if (vs) editor.restoreViewState(vs as never);
  else if (props.cursor) {
    editor.setPosition({
      lineNumber: props.cursor.line,
      column: props.cursor.col,
    });
    editor.revealLineInCenterIfOutsideViewport(props.cursor.line);
  }
  applyPending();
  editor.focus();
}

function belongsToModel(r: NonNullable<typeof pending>): boolean {
  if (!props.model) return false;
  if (r.path === undefined) return true;
  const loc = store.locate(props.model.uri.toString());
  return !!loc && loc.folderId === r.folderId && loc.path === r.path;
}

function applyPending() {
  const r = pending;
  if (!editor || !r || !belongsToModel(r)) return;
  pending = null;
  editor.setPosition({ lineNumber: r.line, column: r.col });
  editor.revealLineInCenter(r.line);
  editor.focus();
}

function doReveal() {
  pending = props.reveal;
  applyPending();
}

onMounted(async () => {
  const monaco = store.monaco;
  if (!monaco || !el.value || disposed) return;
  const css = getComputedStyle(document.documentElement);
  editor = monaco.editor.create(el.value, {
    model: null,
    automaticLayout: true,
    readOnly: props.readOnly,
    minimap: { enabled: false },
    scrollBeyondLastLine: false,
    fontFamily: css.getPropertyValue("--font-mono").trim() || "monospace",
    fontSize: 13,
    renderWhitespace: "selection",
    tabSize: 4,
    theme: "legos",
  });
  const { KeyMod, KeyCode } = monaco;
  editor.addCommand(KeyMod.CtrlCmd | KeyCode.KeyS, () => emit("save"));
  editor.addCommand(KeyMod.CtrlCmd | KeyMod.Alt | KeyCode.KeyS, () =>
    emit("saveAll"),
  );
  editor.addCommand(KeyMod.CtrlCmd | KeyCode.KeyP, () => emit("quick"));
  editor.addCommand(KeyMod.CtrlCmd | KeyCode.KeyW, () => emit("closeTab"));
  editor.onDidChangeCursorPosition((e) =>
    emit("cursor", e.position.lineNumber, e.position.column),
  );
  attach(props.model, null);
  doReveal();
});

watch(
  () => props.model,
  (m, prev) => attach(m, prev),
);
watch(
  () => props.readOnly,
  (r) => editor?.updateOptions({ readOnly: r }),
);
watch(
  () => props.reveal?.n,
  () => doReveal(),
);

onBeforeUnmount(() => {
  disposed = true;
  changeSub?.dispose();
  editor?.setModel(null);
  editor?.dispose();
  editor = null;
  views.clear();
});
</script>

<template>
  <div ref="el" class="mh" data-test="monaco-host"></div>
</template>

<style scoped>
.mh {
  width: 100%;
  height: 100%;
  min-height: 0;
}
</style>
