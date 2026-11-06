<script setup lang="ts">
import { computed, ref } from "vue";
import { ChevronDown, ChevronRight } from "lucide-vue-next";
import { splitRows, type DiffFile } from "./diff";
const props = defineProps<{ file: DiffFile; mode: "unified" | "split" }>();
// Over this many changed lines the body waits for a click, as on GitHub.
const BIG = 300;
const open = ref(!props.file.lockfile),
  loaded = ref(props.file.add + props.file.del <= BIG);
const sign = { add: "+", del: "−", ctx: " " };
const split = computed(() =>
  props.mode === "split" ? props.file.hunks.map(splitRows) : [],
);
const bodyNote = computed(() =>
  props.file.binary
    ? "Binary file not shown."
    : props.file.hunks.length
      ? ""
      : props.file.status === "renamed"
        ? "Renamed without content changes."
        : "No textual changes.",
);
function expand() {
  open.value = true;
  loaded.value = true;
}
defineExpose({ expand });
</script>
<template>
  <section class="fx-dfile" data-test="diff-file">
    <header>
      <button
        class="fx-icon"
        :aria-expanded="open"
        :aria-label="(open ? 'Collapse ' : 'Expand ') + file.path"
        @click="open = !open"
      >
        <ChevronDown v-if="open" class="icon" /><ChevronRight
          v-else
          class="icon"
        />
      </button>
      <span class="path">{{ file.path }}</span
      ><span v-if="file.status && file.status !== 'modified'" class="fx-code">{{
        file.status === "renamed" && file.oldPath
          ? "renamed from " + file.oldPath
          : file.status
      }}</span
      ><span v-if="file.lockfile" class="fx-code">lockfile</span
      ><span v-if="file.note" class="fx-code">{{ file.note }}</span
      ><span class="fx-add">+{{ file.add }}</span
      ><span class="fx-del">−{{ file.del }}</span>
    </header>
    <p v-if="open && !loaded" class="fx-dnote" data-test="diff-load">
      <button class="lg-btn small" @click="loaded = true">
        Load diff ({{ file.add + file.del }} changed lines)
      </button>
    </p>
    <div v-else-if="open && file.hunks.length" class="fx-dtable" :class="mode">
      <template v-for="(h, hi) in file.hunks" :key="hi">
        <div class="fx-hh">{{ h.header }}</div>
        <template v-if="mode === 'unified'">
          <div v-for="(l, li) in h.lines" :key="li" class="fx-dl" :class="l.t">
            <span class="ln">{{ l.o ?? "" }}</span
            ><span class="ln">{{ l.n ?? "" }}</span
            ><code
              ><i class="sg">{{ sign[l.t] }}</i
              >{{ l.s }}</code
            >
          </div>
        </template>
        <template v-else>
          <div v-for="(row, ri) in split[hi]" :key="ri" class="fx-sl">
            <span class="ln">{{ row[0]?.o ?? "" }}</span
            ><code :class="row[0]?.t ?? 'empty'"
              ><i v-if="row[0]" class="sg">{{ sign[row[0].t] }}</i
              >{{ row[0]?.s }}</code
            ><span class="ln">{{ row[1]?.n ?? "" }}</span
            ><code :class="row[1]?.t ?? 'empty'"
              ><i v-if="row[1]" class="sg">{{ sign[row[1].t] }}</i
              >{{ row[1]?.s }}</code
            >
          </div>
        </template>
      </template>
    </div>
    <p
      v-if="open && loaded && (bodyNote || file.truncated)"
      class="tiny muted fx-dnote"
      data-test="diff-note"
    >
      {{ bodyNote }}
      <template v-if="file.truncated">This file's diff is truncated.</template>
    </p>
  </section>
</template>
