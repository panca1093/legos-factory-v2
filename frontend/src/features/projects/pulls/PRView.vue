<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import { RouterLink, useRoute } from "vue-router";
import { ExternalLink, RefreshCw } from "lucide-vue-next";
import "../../factory/factory.css";
import "./pulls.css";
import { eventTarget, usePRRefresh, useReviews } from "../../../stores/reviews";
import { useToast } from "../../../composables/useToast";
import { renderMarkdown } from "../../knowledge/notes";
import { fileTree, scrollToDraft, scrollToFile } from "./diff";
import DiffFile from "./DiffFile.vue";
import DraftCard from "./DraftCard.vue";
import { connectionsPath } from "../../../types/review";

const route = useRoute(),
  store = useReviews(),
  { toast } = useToast();
const projectId = computed(() => String(route.params.id));
const number = computed(() => Number(route.params.number));
// Same order as the Changes tree, so scrolling the diff walks the tree top to bottom.
const files = computed(() =>
  fileTree(store.files, new Set()).flatMap((r) => (r.file ? [r.file] : [])),
);

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
async function refetch() {
  await store.refetch();
  if (store.error) toast(store.error);
}
const general = computed(() => store.drafts.filter((d) => !d.inline));

// Viewed files start collapsed; ticking Viewed later collapses, unticking reopens.
const collapsed = ref(new Set<string>());
const forced = ref(new Set<string>());
let seededFor = "";
const was = new Map<string, boolean>();
watch(
  () => store.files,
  (fs) => {
    const key = `${store.projectId}#${store.number}`;
    if (key !== seededFor) {
      if (!fs.length) return;
      seededFor = key;
      was.clear();
      forced.value = new Set();
      collapsed.value = new Set(fs.filter((f) => f.viewed).map((f) => f.path));
    } else {
      const next = new Set(collapsed.value);
      for (const f of fs) {
        const prev = was.get(f.path);
        if (prev === undefined || prev === f.viewed) continue;
        if (f.viewed) next.add(f.path);
        else next.delete(f.path);
      }
      collapsed.value = next;
    }
    for (const f of fs) was.set(f.path, f.viewed);
  },
  { immediate: true },
);
function toggle(path: string) {
  const next = new Set(collapsed.value);
  if (!next.delete(path)) next.add(path);
  collapsed.value = next;
}

// ?file= and ?draft= (set by the tree and the findings) open that file and scroll to it.
async function reveal(path: string, draft: string) {
  if (!store.files.some((f) => f.path === path)) return;
  if (collapsed.value.has(path)) toggle(path);
  forced.value = new Set(forced.value).add(path);
  await nextTick();
  if (!(draft && scrollToDraft(draft))) scrollToFile(path);
}
watch(
  () =>
    [
      !store.loading && store.files.length > 0,
      route.query.file,
      route.query.draft,
      store.focusSeq,
    ] as const,
  ([ready, f, d]) => {
    if (!ready) return;
    const draft = typeof d === "string" ? d : "";
    const path =
      store.drafts.find((x) => x.id === draft && x.inline)?.path ??
      (typeof f === "string" ? f : "");
    if (path) void reveal(path, draft);
    else if (draft) scrollToDraft(draft);
  },
  { immediate: true, flush: "post" },
);

// The sticky header's height offsets file headers and scroll targets below it.
const head = ref<HTMLElement>();
const headH = ref(0);
let ro: ResizeObserver | undefined;
watch(head, (el) => {
  ro?.disconnect();
  if (!el || typeof ResizeObserver === "undefined") return;
  ro = new ResizeObserver(() => (headH.value = el.offsetHeight));
  ro.observe(el);
});
// The file at the top of the diff marks its tree row.
let frame = 0;
function spy() {
  if (frame) return;
  frame = requestAnimationFrame(() => {
    frame = 0;
    let cur = "";
    const picked = route.query.file;
    for (const el of document.querySelectorAll<HTMLElement>(
      ".prv [data-path]",
    )) {
      const r = el.getBoundingClientRect();
      // A picked file near the page end never reaches the top; keep it marked while on screen.
      if (
        el.dataset.path === picked &&
        r.top < innerHeight &&
        r.bottom > headH.value
      ) {
        cur = picked;
        break;
      }
      if (r.top <= headH.value + 48) cur = el.dataset.path ?? "";
    }
    store.current = cur;
  });
}
onMounted(() => window.addEventListener("scroll", spy, { passive: true }));
onBeforeUnmount(() => {
  window.removeEventListener("scroll", spy);
  cancelAnimationFrame(frame);
  ro?.disconnect();
});
const ciLabel = (ci: string) =>
  ({
    passing: "CI passing",
    failing: "CI failing",
    running: "CI running",
    none: "No CI",
  })[ci] ?? ci;
</script>
<template>
  <div class="fx-page prv" :style="{ '--prv-head-h': `${headH}px` }">
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
      <div ref="head" class="prv-stick">
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
              <button
                type="button"
                class="prv-refetch"
                data-test="pr-refetch"
                :disabled="store.refetching"
                @click="refetch"
              >
                <RefreshCw
                  class="icon"
                  :class="{ spin: store.refetching }"
                  aria-hidden="true"
                />{{ store.refetching ? "Refreshing…" : "Refresh" }}
              </button>
            </div>
            <p
              v-if="detail.identity"
              class="small muted"
              data-test="pr-identity"
            >
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
          <p
            v-if="store.stale"
            class="notice warn prv-wide"
            data-test="pr-stale"
          >
            PR updated since this review. Drafts on changed files are outdated;
            re-run the AI review.
          </p>
          <details v-if="pr.body" class="prv-wide">
            <summary>Description</summary>
            <!-- eslint-disable-next-line vue/no-v-html -->
            <div class="md" v-html="bodyHtml"></div>
          </details>
        </section>
      </div>

      <section v-if="general.length" class="prv-card" data-test="pr-general">
        <h2 class="prv-h">General</h2>
        <DraftCard v-for="d in general" :key="d.id" :draft="d" />
      </section>

      <section
        v-if="store.filesError || !files.length"
        class="prv-card prv-diff"
        data-test="pr-diff"
      >
        <p v-if="store.filesError" class="notice warn" role="alert">
          Could not load files. {{ store.filesError }}
        </p>
        <p v-else class="notice">No changed files.</p>
      </section>
      <div v-else class="prv-files" data-test="pr-diff">
        <DiffFile
          v-for="f in files"
          :key="f.path"
          :file="f"
          :project-id="projectId"
          :head="pr.head"
          :host-url="safeUrl(pr.url)"
          :collapsed="collapsed.has(f.path)"
          :force="forced.has(f.path)"
          @toggle="toggle(f.path)"
        />
      </div>
    </template>
  </div>
</template>
