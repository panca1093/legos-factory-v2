<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { ExternalLink, MessageSquarePlus } from "lucide-vue-next";
import "../../factory/factory.css";
import "./pulls.css";
import { eventTarget, usePRRefresh, useReviews } from "../../../stores/reviews";
import { useToast } from "../../../composables/useToast";
import { renderMarkdown } from "../../knowledge/notes";
import { parsePatch, scrollToDraft } from "./diff";
import DraftCard from "./DraftCard.vue";
import { connectionsPath, type PRDraft } from "../../../types/review";

const route = useRoute(),
  store = useReviews(),
  { toast } = useToast();
const projectId = computed(() => String(route.params.id));
const number = computed(() => Number(route.params.number));
const file = computed(() => {
  const want = route.query.file ?? store.firstFile;
  return (
    store.files.find((f) => f.path === want) ?? store.files[0] ?? undefined
  );
});
const hunks = computed(() => (file.value ? parsePatch(file.value.patch) : []));

watch(
  [projectId, number],
  () => void store.load(projectId.value, number.value),
  { immediate: true },
);
usePRRefresh(
  () => void store.refresh(),
  (ev) => {
    const t = eventTarget(ev);
    return t.project === projectId.value && t.number === number.value;
  },
);
let poll: ReturnType<typeof setInterval> | undefined;
watch(
  () => store.running,
  (r) => {
    clearInterval(poll);
    poll = r ? setInterval(() => void store.refresh(), 5000) : undefined;
  },
  { immediate: true },
);
onBeforeUnmount(() => clearInterval(poll));

const safeUrl = (u: string) => (/^https?:\/\//i.test(u) ? u : "");
const pr = computed(() => store.pr);
const detail = computed(() => store.detail);
const bodyHtml = computed(() => renderMarkdown(pr.value?.body ?? ""));
const canRun = computed(() => !!detail.value?.ai.ok && !store.running);
const runLabel = computed(() => {
  if (store.running) return "AI review running…";
  return store.run ? "Re-run AI review" : "Run AI review";
});
const reason = computed(() =>
  detail.value && !detail.value.ai.ok ? (detail.value.ai.reason ?? "") : "",
);
async function run() {
  try {
    await store.runAI();
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  }
}

const visible = (d: PRDraft) =>
  d.status !== "superseded" || d.path === file.value?.path;
const fileDrafts = computed(() =>
  store.drafts.filter(
    (d) => d.inline && d.path === file.value?.path && visible(d),
  ),
);
const anchored = computed(() => {
  const map = new Map<string, PRDraft[]>();
  const loose: PRDraft[] = [];
  for (const d of fileDrafts.value) {
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
const general = computed(() => store.drafts.filter((d) => !d.inline));

// ?draft= (set by the Review card's findings) scrolls to that card once, as soon as it is on screen.
let scrolledTo = "";
watch(
  () => [route.query.draft, file.value?.path, store.drafts.length] as const,
  async ([id]) => {
    if (typeof id !== "string" || !id || id === scrolledTo) return;
    await nextTick();
    if (scrollToDraft(id)) scrolledTo = id;
  },
  { flush: "post" },
);

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
  document.querySelector<HTMLTextAreaElement>(".dv-compose textarea")?.focus();
}
async function saveComment() {
  const c = composing.value;
  if (!c || !file.value || !text.value.trim() || saving.value) return;
  saving.value = true;
  try {
    await store.addComment({
      path: file.value.path,
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
watch(
  () => file.value?.path,
  () => (composing.value = null),
);
const ciLabel = (ci: string) =>
  ({
    passing: "CI passing",
    failing: "CI failing",
    running: "CI running",
    none: "No CI",
  })[ci] ?? ci;
</script>
<template>
  <div class="fx-page prv">
    <p v-if="store.loading && !pr" class="notice" data-test="pr-loading">
      Loading…
    </p>
    <div v-else-if="store.notFound" class="panel empty" data-test="pr-missing">
      <h2>We couldn’t find that pull request.</h2>
      <p class="help">It may be closed, or you may not have access to it.</p>
      <RouterLink
        class="lg-btn primary section-gap"
        :to="`/projects/${projectId}/pulls`"
        >All pull requests</RouterLink
      >
    </div>
    <div
      v-else-if="store.error && !pr"
      class="error-state"
      role="alert"
      data-test="pr-error"
    >
      <h2>We couldn’t load this pull request.</h2>
      <p class="small">{{ store.error }}</p>
      <button type="button" class="lg-btn" @click="store.refresh">
        Try again
      </button>
    </div>
    <template v-else-if="pr && detail">
      <section class="prv-card prv-head" data-test="pr-header">
        <div class="prv-title">
          <h1>
            {{ pr.title }} <span class="mono muted">#{{ pr.number }}</span>
            <span v-if="pr.draft" class="prs-draft">Draft</span>
          </h1>
          <div class="prs-meta small muted">
            <span>{{ pr.author.login }}</span>
            <span class="mono">{{ pr.head }} → {{ pr.base }}</span>
            <span class="prv-ci" :class="pr.ci" data-test="pr-ci">{{
              ciLabel(pr.ci)
            }}</span>
            <span class="prv-add">+{{ pr.additions }}</span>
            <span class="prv-del">−{{ pr.deletions }}</span>
            <span>{{ pr.changed_files }} files</span>
            <a
              v-if="safeUrl(pr.url)"
              :href="safeUrl(pr.url)"
              target="_blank"
              rel="noopener noreferrer"
              data-test="pr-host-link"
              >Open on host<ExternalLink class="icon"
            /></a>
          </div>
          <p v-if="detail.identity" class="small muted" data-test="pr-identity">
            Posting as <strong>{{ detail.identity.login }}</strong> ·
            {{ detail.identity.host }} ({{
              detail.identity.source === "connection"
                ? "connection"
                : detail.identity.source
            }})
          </p>
          <p v-else class="small muted">
            No review account.
            <RouterLink :to="connectionsPath">Connect an account</RouterLink>
          </p>
        </div>
        <div class="prv-run">
          <button
            type="button"
            class="lg-btn primary"
            data-test="pr-run"
            :disabled="!canRun"
            :title="reason || undefined"
            @click="run"
          >
            {{ runLabel }}
          </button>
          <p v-if="reason" class="small muted" data-test="pr-run-reason">
            {{ reason }}
          </p>
          <p
            v-else-if="store.run?.state === 'failed'"
            class="small prv-bad"
            role="alert"
          >
            Last run failed: {{ store.run.error }}
          </p>
        </div>
        <p v-if="store.stale" class="notice warn prv-wide" data-test="pr-stale">
          PR updated since this review. Drafts on changed files are outdated;
          re-run the AI review.
        </p>
        <details v-if="pr.body" class="prv-wide">
          <summary>Description</summary>
          <!-- eslint-disable-next-line vue/no-v-html -->
          <div class="md" v-html="bodyHtml"></div>
        </details>
      </section>

      <section v-if="general.length" class="prv-card" data-test="pr-general">
        <h2 class="prv-h">General</h2>
        <DraftCard v-for="d in general" :key="d.id" :draft="d" />
      </section>

      <section class="prv-card prv-diff" data-test="pr-diff">
        <p v-if="store.filesError" class="notice warn" role="alert">
          Could not load files. {{ store.filesError }}
        </p>
        <p v-else-if="!file" class="notice">No changed files.</p>
        <template v-else>
          <h2 class="prv-h mono" data-test="diff-file">
            {{ file.path }}
            <span v-if="file.status === 'renamed'" class="muted small"
              >(from {{ file.old_path }})</span
            >
          </h2>
          <p v-if="!file.patch" class="notice" data-test="diff-empty">
            {{
              file.truncated
                ? "This diff is too large to show here."
                : "No text diff for this file (binary or empty)."
            }}
            <a
              v-if="safeUrl(pr.url)"
              :href="safeUrl(pr.url)"
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
                  >{{ l.s
                  }}<RouterLink
                    v-if="l.n !== undefined"
                    class="dv-open"
                    :to="{
                      path: `/projects/${projectId}/code`,
                      query: {
                        file: file.path,
                        line: String(l.n),
                        pr_head: pr.head,
                      },
                    }"
                    data-test="open-in-code"
                    >Open in Code</RouterLink
                  ></span
                >
              </div>
              <div
                v-if="
                  anchored.map.get(`${hi}:${li}`) ||
                  composeKey === `${hi}:${li}`
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
      </section>
    </template>
  </div>
</template>
