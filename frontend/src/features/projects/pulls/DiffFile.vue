<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import { RouterLink } from "vue-router";
import { ChevronDown, ChevronRight, MessageSquarePlus } from "lucide-vue-next";
import { useReviews } from "../../../stores/reviews";
import { useToast } from "../../../composables/useToast";
import { parsePatch } from "./diff";
import { highlightHunks, type Seg } from "./highlight";
import DraftCard from "./DraftCard.vue";
import type { PRDraft, PRFile } from "../../../types/review";

const props = defineProps<{
  file: PRFile;
  projectId: string;
  head: string;
  hostUrl: string;
  collapsed: boolean;
  force: boolean;
}>();
const emit = defineEmits<{ toggle: [] }>();
const store = useReviews(),
  { toast } = useToast();
const root = ref<HTMLElement>();

// Bodies render once near the viewport, so a PR with many files opens fast.
const seen = ref(false);
let io: IntersectionObserver | undefined;
onMounted(() => {
  if (typeof IntersectionObserver === "undefined" || !root.value) {
    seen.value = true;
    return;
  }
  io = new IntersectionObserver(
    (es) => {
      if (es.some((e) => e.isIntersecting)) {
        seen.value = true;
        io?.disconnect();
      }
    },
    { rootMargin: "1200px 0px" },
  );
  io.observe(root.value);
});
onBeforeUnmount(() => io?.disconnect());
const shown = computed(() => !props.collapsed && (seen.value || props.force));

const hunks = computed(() => parsePatch(props.file.patch));
const rough = computed(
  () => hunks.value.reduce((n, h) => n + h.lines.length + 1, 0) * 20,
);
const tones = ref<Seg[][][] | null>(null);
watch(
  () => [shown.value, hunks.value] as const,
  async ([on, hs]) => {
    if (!on) return;
    const got = await highlightHunks(props.file.path, hs);
    if (hs === hunks.value) tones.value = got;
  },
  { immediate: true },
);
const segs = (hi: number, li: number, s: string): Seg[] =>
  tones.value?.[hi]?.[li] ?? [{ t: s, c: "" }];

const drafts = computed(() =>
  store.drafts.filter((d) => d.inline && d.path === props.file.path),
);
const live = computed(
  () =>
    drafts.value.filter(
      (d) => d.status !== "discarded" && d.status !== "superseded",
    ).length,
);
const anchored = computed(() => {
  const map = new Map<string, PRDraft[]>();
  const loose: PRDraft[] = [];
  for (const d of drafts.value) {
    let key = "";
    hunks.value.forEach((h, hi) =>
      h.lines.forEach((l, li) => {
        if (key) return;
        const hit =
          d.side === "old"
            ? l.t !== "add" && l.o === (d.old_line || d.line)
            : l.t !== "del" && l.n === d.line;
        if (hit) key = `${hi}:${li}`;
      }),
    );
    if (key) map.set(key, [...(map.get(key) ?? []), d]);
    else loose.push(d);
  }
  return { map, loose };
});

async function viewed(v: boolean) {
  try {
    await store.setViewed(props.file.path, v);
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  }
}

// A comment of the user's own on one diff line: removed lines anchor on the old side, the rest on the new.
type Line = { t: string; o?: number; n?: number };
const composing = ref<{ side: "new" | "old"; line: number } | null>(null);
// Placed by line, not row index, so a refreshed patch keeps the composer under its line.
const composeKey = computed(() => {
  const c = composing.value;
  if (!c) return "";
  for (const [hi, h] of hunks.value.entries())
    for (const [li, l] of h.lines.entries())
      if (
        c.side === "old"
          ? l.t === "del" && l.o === c.line
          : l.t !== "del" && l.n === c.line
      )
        return `${hi}:${li}`;
  return "";
});
const composeLabel = computed(() =>
  composing.value
    ? `Your comment on ${composing.value.side === "old" ? "removed line" : "line"} ${composing.value.line}`
    : "",
);
const lineLabel = (l: Line) =>
  l.t === "del" ? `removed line ${l.o}` : `line ${l.n}`;
const text = ref(""),
  saving = ref(false);
async function compose(l: Line) {
  const side = l.t === "del" ? "old" : "new";
  const line = side === "old" ? l.o : l.n;
  if (line === undefined) return;
  composing.value = { side, line };
  text.value = "";
  await nextTick();
  root.value
    ?.querySelector<HTMLTextAreaElement>(".dv-compose textarea")
    ?.focus();
}
async function saveComment() {
  const c = composing.value;
  if (!c || !text.value.trim() || saving.value) return;
  saving.value = true;
  try {
    await store.addComment({
      path: props.file.path,
      side: c.side,
      line: c.line,
      body: text.value,
    });
    composing.value = null;
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  } finally {
    saving.value = false;
  }
}
</script>
<template>
  <section
    ref="root"
    class="prv-card df"
    :data-path="file.path"
    data-test="diff-section"
  >
    <header class="df-head">
      <button
        type="button"
        class="df-toggle"
        :aria-expanded="!collapsed"
        :aria-label="`${collapsed ? 'Show' : 'Hide'} ${file.path}`"
        data-test="diff-toggle"
        @click="emit('toggle')"
      >
        <ChevronRight v-if="collapsed" class="icon" /><ChevronDown
          v-else
          class="icon"
        />
      </button>
      <h2 class="prv-h mono" data-test="diff-file" :title="file.path">
        {{ file.path }}
        <span v-if="file.status === 'renamed'" class="muted small"
          >(from {{ file.old_path }})</span
        >
      </h2>
      <span v-if="live" class="ft-cnt" data-test="diff-drafts">{{ live }}</span>
      <span class="ft-diff"
        ><b class="prv-add">+{{ file.additions }}</b>
        <b class="prv-del">−{{ file.deletions }}</b></span
      >
      <label class="df-viewed small">
        <input
          type="checkbox"
          :checked="file.viewed"
          data-test="diff-viewed"
          @change="viewed(($event.target as HTMLInputElement).checked)"
        />
        Viewed
      </label>
    </header>
    <div
      v-if="!collapsed"
      class="df-body"
      :style="shown ? undefined : { minHeight: `${rough}px` }"
    >
      <template v-if="shown">
        <p v-if="!file.patch" class="notice" data-test="diff-empty">
          {{
            file.truncated
              ? "This diff is too large to show here."
              : "No text diff for this file (binary or empty)."
          }}
          <a
            v-if="hostUrl"
            :href="hostUrl"
            target="_blank"
            rel="noopener noreferrer"
            >View on host</a
          >
        </p>
        <div v-if="anchored.loose.length" class="prv-loose">
          <p class="small muted">Comments not on a visible line</p>
          <DraftCard v-for="d in anchored.loose" :key="d.id" :draft="d" />
        </div>
        <div v-for="(h, hi) in hunks" :key="hi" class="dv">
          <div class="dv-hunk mono">{{ h.header }}</div>
          <template v-for="(l, li) in h.lines" :key="li">
            <div class="dv-line mono" :class="l.t">
              <button
                type="button"
                class="dv-add"
                :aria-label="`Comment on ${lineLabel(l)}`"
                data-test="diff-add-comment"
                @click="compose(l)"
              >
                <MessageSquarePlus class="icon" aria-hidden="true" />
              </button>
              <span class="dv-no">{{ l.o ?? "" }}</span>
              <span class="dv-no">{{ l.n ?? "" }}</span>
              <span class="dv-mark">{{
                l.t === "add" ? "+" : l.t === "del" ? "−" : " "
              }}</span>
              <span class="dv-code"
                ><span
                  v-for="(g, gi) in segs(hi, li, l.s)"
                  :key="gi"
                  :class="g.c || undefined"
                  >{{ g.t }}</span
                ><RouterLink
                  v-if="l.n !== undefined"
                  class="dv-open"
                  :to="{
                    path: `/projects/${projectId}/code`,
                    query: {
                      file: file.path,
                      line: String(l.n),
                      pr_head: head,
                    },
                  }"
                  data-test="open-in-code"
                  >Open in Code</RouterLink
                ></span
              >
            </div>
            <div
              v-if="
                anchored.map.get(`${hi}:${li}`) || composeKey === `${hi}:${li}`
              "
              class="dv-slot"
            >
              <DraftCard
                v-for="d in anchored.map.get(`${hi}:${li}`) ?? []"
                :key="d.id"
                :draft="d"
              />
              <form
                v-if="composeKey === `${hi}:${li}`"
                class="dv-compose"
                data-test="diff-compose"
                @submit.prevent="saveComment"
              >
                <textarea
                  v-model="text"
                  rows="3"
                  :aria-label="composeLabel"
                  placeholder="Leave a comment. It posts with your review."
                  @keydown.meta.enter.prevent="saveComment"
                  @keydown.ctrl.enter.prevent="saveComment"
                  @keydown.esc="composing = null"
                ></textarea>
                <div class="dc-actions">
                  <button
                    type="submit"
                    class="lg-btn small primary"
                    data-test="diff-compose-save"
                    :disabled="saving || !text.trim()"
                  >
                    Add comment
                  </button>
                  <button
                    type="button"
                    class="lg-btn small ghost"
                    @click="composing = null"
                  >
                    Cancel
                  </button>
                </div>
              </form>
            </div>
          </template>
        </div>
      </template>
    </div>
  </section>
</template>
