<script setup lang="ts">
import { computed, ref } from "vue";
import type { DiffFile } from "./diff";
import DiffFileView from "./DiffFileView.vue";
const props = defineProps<{
  files: DiffFile[];
  resetKey?: string;
  meta?: {
    base?: string;
    head?: string;
    revision?: number;
    truncated?: boolean;
  };
}>();
const mode = ref<"unified" | "split">("unified");
const views: Record<string, InstanceType<typeof DiffFileView> | null> = {};
const totals = computed(() => ({
  add: props.files.reduce((n, f) => n + f.add, 0),
  del: props.files.reduce((n, f) => n + f.del, 0),
}));
const short = (sha?: string) => sha?.slice(0, 7);
function jump(i: number) {
  views[props.files[i].path]?.expand();
  document.getElementById("diff-file-" + i)?.scrollIntoView({ block: "start" });
}
</script>
<template>
  <div class="fx-diff">
    <div class="fx-diff-bar">
      <span class="small muted" data-test="diff-total"
        >{{ files.length }} {{ files.length === 1 ? "file" : "files" }} changed
        ·
        <span class="fx-add">+{{ totals.add }}</span>
        <span class="fx-del"> −{{ totals.del }}</span></span
      >
      <span v-if="meta?.head" class="tiny muted fx-code" data-test="diff-range"
        >{{ short(meta.base) ? short(meta.base) + "…" : ""
        }}{{ short(meta.head)
        }}<template v-if="meta.revision !== undefined">
          · revision {{ meta.revision }}</template
        ></span
      >
      <div class="fx-seg" role="group" aria-label="Diff layout">
        <button
          :aria-pressed="mode === 'unified'"
          :class="{ on: mode === 'unified' }"
          @click="mode = 'unified'"
        >
          Unified
        </button>
        <button
          :aria-pressed="mode === 'split'"
          :class="{ on: mode === 'split' }"
          @click="mode = 'split'"
        >
          Split
        </button>
      </div>
    </div>
    <p v-if="meta?.truncated" class="notice" data-test="diff-truncated">
      This diff is too large to show in full; some files or lines are omitted.
    </p>
    <div class="fx-diff-body">
      <nav class="fx-files" aria-label="Changed files">
        <button
          v-for="(f, i) in files"
          :key="f.path"
          :title="f.path"
          @click="jump(i)"
        >
          <span class="fn">{{ f.path.split("/").pop() }}</span
          ><span class="fx-add">+{{ f.add }}</span
          ><span class="fx-del">−{{ f.del }}</span>
        </button>
      </nav>
      <div class="fx-diff-files">
        <div
          v-for="(f, i) in files"
          :id="'diff-file-' + i"
          :key="(resetKey ?? '') + ':' + f.path"
        >
          <DiffFileView
            :ref="(el) => (views[f.path] = el as never)"
            :file="f"
            :mode="mode"
          />
        </div>
      </div>
    </div>
  </div>
</template>
