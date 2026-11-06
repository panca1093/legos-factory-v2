<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { ChevronDown, ChevronRight } from "lucide-vue-next";
import { splitRows, type DiffFile } from "./diff";
const props = defineProps<{
  files: DiffFile[];
  meta?: {
    base?: string;
    head?: string;
    revision?: number;
    truncated?: boolean;
  };
}>();
const mode = ref<"unified" | "split">("unified");
const collapsed = reactive<Record<string, boolean>>({});
const totals = computed(() => ({
  add: props.files.reduce((n, f) => n + f.add, 0),
  del: props.files.reduce((n, f) => n + f.del, 0),
}));
const sign = { add: "+", del: "−", ctx: " " };
const isCollapsed = (f: DiffFile) => collapsed[f.path] ?? !!f.lockfile;
const short = (sha?: string) => sha?.slice(0, 7);
const bodyNote = (f: DiffFile) =>
  f.binary
    ? "Binary file not shown."
    : f.hunks.length
      ? ""
      : f.status === "renamed"
        ? "Renamed without content changes."
        : "No textual changes.";
function jump(i: number) {
  collapsed[props.files[i].path] = false;
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
        <section
          v-for="(f, i) in files"
          :id="'diff-file-' + i"
          :key="f.path"
          class="fx-dfile"
          data-test="diff-file"
        >
          <header>
            <button
              class="fx-icon"
              :aria-expanded="!isCollapsed(f)"
              :aria-label="(isCollapsed(f) ? 'Expand ' : 'Collapse ') + f.path"
              @click="collapsed[f.path] = !isCollapsed(f)"
            >
              <ChevronRight v-if="isCollapsed(f)" class="icon" /><ChevronDown
                v-else
                class="icon"
              />
            </button>
            <span class="path">{{ f.path }}</span
            ><span v-if="f.status && f.status !== 'modified'" class="fx-code">{{
              f.status === "renamed" && f.oldPath
                ? "renamed from " + f.oldPath
                : f.status
            }}</span
            ><span v-if="f.lockfile" class="fx-code">lockfile</span
            ><span v-if="f.note" class="fx-code">{{ f.note }}</span
            ><span class="fx-add">+{{ f.add }}</span
            ><span class="fx-del">−{{ f.del }}</span>
          </header>
          <div
            v-if="!isCollapsed(f) && f.hunks.length"
            class="fx-dtable"
            :class="mode"
          >
            <template v-for="(h, hi) in f.hunks" :key="hi">
              <div class="fx-hh">{{ h.header }}</div>
              <template v-if="mode === 'unified'">
                <div
                  v-for="(l, li) in h.lines"
                  :key="li"
                  class="fx-dl"
                  :class="l.t"
                >
                  <span class="ln">{{ l.o ?? "" }}</span
                  ><span class="ln">{{ l.n ?? "" }}</span
                  ><code
                    ><i class="sg">{{ sign[l.t] }}</i
                    >{{ l.s }}</code
                  >
                </div>
              </template>
              <template v-else>
                <div v-for="(row, ri) in splitRows(h)" :key="ri" class="fx-sl">
                  <span class="ln">{{ row[0]?.o ?? "" }}</span
                  ><code :class="row[0]?.t ?? 'empty'">{{ row[0]?.s }}</code
                  ><span class="ln">{{ row[1]?.n ?? "" }}</span
                  ><code :class="row[1]?.t ?? 'empty'">{{ row[1]?.s }}</code>
                </div>
              </template>
            </template>
          </div>
          <p
            v-if="!isCollapsed(f) && (bodyNote(f) || f.truncated)"
            class="tiny muted fx-dnote"
            data-test="diff-note"
          >
            {{ bodyNote(f) }}
            <template v-if="f.truncated"
              >This file's diff is truncated.</template
            >
          </p>
        </section>
      </div>
    </div>
  </div>
</template>
