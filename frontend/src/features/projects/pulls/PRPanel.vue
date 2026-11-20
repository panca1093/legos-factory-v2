<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ChevronDown, ChevronRight } from "lucide-vue-next";
import "./pulls.css";
import { useReviews } from "../../../stores/reviews";
import { useToast } from "../../../composables/useToast";
import AppDialog from "../../../components/ui/AppDialog.vue";
import PanelCard from "../PanelCard.vue";
import { fileTree, scrollToDraft } from "./diff";
import type { PostConflict } from "../../../stores/reviews";
import type { PostExpect, Verdict } from "../../../types/review";

const route = useRoute(),
  router = useRouter(),
  store = useReviews(),
  { toast } = useToast();
const closed = ref(new Set<string>());
const rows = computed(() => fileTree(store.files, closed.value));
const current = computed(
  () => (route.query.file as string | undefined) ?? store.firstFile,
);
const draftCount = (path: string) =>
  store.drafts.filter(
    (d) =>
      d.path === path &&
      d.inline &&
      d.status !== "discarded" &&
      d.status !== "superseded",
  ).length;
function toggleDir(path: string) {
  const next = new Set(closed.value);
  if (!next.delete(path)) next.add(path);
  closed.value = next;
}
function pick(path: string) {
  void router.push({ query: { ...route.query, file: path } });
}
async function viewed(path: string, v: boolean) {
  try {
    await store.setViewed(path, v);
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  }
}

const ORDER: Record<string, number> = {
  pending: 0,
  accepted: 1,
  outdated: 2,
  posted: 3,
  discarded: 4,
};
const SEV: Record<string, number> = { blocker: 0, major: 1, minor: 2, info: 3 };
const findings = computed(() =>
  store.drafts
    .filter((d) => d.status !== "superseded")
    .slice()
    .sort(
      (a, b) =>
        (ORDER[a.status] ?? 9) - (ORDER[b.status] ?? 9) ||
        (SEV[a.severity] ?? 9) - (SEV[b.severity] ?? 9) ||
        a.path.localeCompare(b.path) ||
        a.line - b.line,
    ),
);
const where = (d: { path: string; line: number; inline: boolean }) =>
  d.inline ? `${d.path.split("/").pop()}:${d.line}` : "General";
function goTo(d: { id: string; path: string; inline: boolean }) {
  if (route.query.draft === d.id) {
    scrollToDraft(d.id);
    return;
  }
  void router.push({
    query: {
      ...route.query,
      ...(d.inline ? { file: d.path } : {}),
      draft: d.id,
    },
  });
}

const detail = computed(() => store.detail);
const summary = ref(""),
  touched = ref(false),
  verdict = ref<Verdict>("comment");
watch(
  () => [store.review?.summary, store.run?.id, store.run?.summary] as const,
  () => {
    if (touched.value) return;
    summary.value = store.review?.summary || store.run?.summary || "";
  },
  { immediate: true },
);
watch(
  () => store.review?.verdict,
  (v) => {
    if (v) verdict.value = v as Verdict;
  },
  { immediate: true },
);
watch(
  () => `${store.projectId}#${store.number}`,
  () => {
    touched.value = false;
    summary.value = store.review?.summary || store.run?.summary || "";
    verdict.value = (store.review?.verdict as Verdict) || "comment";
    store.posted = null;
  },
);
const verdictOpts = computed(() => {
  const v = detail.value?.verdicts;
  return [
    { key: "comment" as Verdict, label: "Comment", ok: true, reason: "" },
    {
      key: "approve" as Verdict,
      label: "Approve",
      ok: v?.approve.ok ?? false,
      reason: v?.approve.reason ?? "",
    },
    {
      key: "request_changes" as Verdict,
      label: "Request changes",
      ok: v?.request_changes.ok ?? false,
      reason: v?.request_changes.reason ?? "",
    },
  ];
});
async function saveSummary() {
  if (!touched.value) return;
  try {
    await store.saveReview({ summary: summary.value });
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  }
}
async function setVerdict(v: Verdict) {
  verdict.value = v;
  try {
    await store.saveReview({ verdict: v });
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
  }
}

const count = computed(() => store.accepted.length);
const failedCount = computed(
  () => store.accepted.filter((d) => d.post_error).length,
);
const canPost = computed(
  () =>
    !!detail.value?.identity &&
    (count.value > 0 || !!summary.value.trim() || verdict.value !== "comment"),
);
const confirming = ref(false),
  conflict = ref<PostConflict | null>(null),
  postError = ref("");
const expect = computed<PostExpect | null>(() =>
  conflict.value
    ? conflict.value.current
    : detail.value?.identity
      ? {
          login: detail.value.identity.login,
          head_sha: detail.value.pr.head_sha,
          comments: count.value,
          verdict: verdict.value,
        }
      : null,
);
async function openConfirm() {
  try {
    await store.saveReview({ summary: summary.value, verdict: verdict.value });
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e));
    return;
  }
  conflict.value = null;
  postError.value = "";
  confirming.value = true;
}
async function confirmPost() {
  if (!expect.value) return;
  postError.value = "";
  try {
    await store.post(expect.value);
    confirming.value = false;
    conflict.value = null;
  } catch (e) {
    const c = (e as { conflict?: PostConflict }).conflict;
    if (c) {
      conflict.value = c;
      void store.refresh();
    } else postError.value = e instanceof Error ? e.message : String(e);
  }
}
const titleOf = (id: string) =>
  store.drafts.find((d) => d.id === id)?.title ?? id;
const counts = computed(() => {
  const c = { blocker: 0, major: 0, minor: 0, info: 0 };
  for (const d of store.live) if (d.run_id !== "") c[d.severity]++;
  return c;
});
const verdictLabel = (v: string) =>
  v === "request_changes" ? "request changes" : v;
const postedHere = computed(
  () =>
    !!store.review?.posted_at &&
    store.review.posted_head_sha === store.pr?.head_sha,
);
</script>
<template>
  <template v-if="store.pr">
    <PanelCard id="pr-changes" title="Changes">
      <p class="small muted" data-test="viewed-count">
        {{ store.viewedCount }} / {{ store.files.length }} viewed
      </p>
      <ul class="ft" data-test="file-tree">
        <li
          v-for="r in rows"
          :key="r.kind + r.path"
          :style="{ '--d': r.depth }"
        >
          <button
            v-if="r.kind === 'dir'"
            type="button"
            class="ft-dir"
            :aria-expanded="!closed.has(r.path)"
            @click="toggleDir(r.path)"
          >
            <ChevronRight v-if="closed.has(r.path)" class="icon" /><ChevronDown
              v-else
              class="icon"
            />{{ r.name }}
          </button>
          <div
            v-else-if="r.file"
            class="ft-file"
            :class="{ cur: r.path === current }"
          >
            <input
              type="checkbox"
              :checked="r.file.viewed"
              :aria-label="`Viewed ${r.path}`"
              data-test="file-viewed"
              @change="
                viewed(r.path, ($event.target as HTMLInputElement).checked)
              "
            />
            <button
              type="button"
              class="ft-name"
              :class="{ unseen: !r.file.viewed }"
              :title="r.path"
              :aria-current="r.path === current ? 'true' : undefined"
              data-test="file-pick"
              @click="pick(r.path)"
            >
              <span class="ft-st" :data-st="r.file.status" aria-hidden="true">{{
                { added: "A", modified: "M", deleted: "D", renamed: "R" }[
                  r.file.status
                ]
              }}</span>{{ r.name }}
            </button>
            <span
              v-if="draftCount(r.path)"
              class="ft-cnt"
              data-test="file-drafts"
              >{{ draftCount(r.path) }}</span
            >
            <span class="ft-diff"
              ><b class="prv-add">+{{ r.file.additions }}</b>
              <b class="prv-del">−{{ r.file.deletions }}</b></span
            >
          </div>
        </li>
      </ul>
      <p v-if="!rows.length" class="small muted">No changed files.</p>
    </PanelCard>
    <PanelCard id="pr-review" title="Review">
      <p v-if="store.running" class="small" data-test="review-running">
        AI review running…
      </p>
      <p
        v-else-if="store.run?.summary"
        class="pr-counts"
        data-test="review-counts"
      >
        <span
          v-for="k in ['blocker', 'major', 'minor', 'info'] as const"
          :key="k"
          class="dc-sev"
          :data-sev="k"
          :class="{ zero: !counts[k] }"
          >{{ counts[k] }} {{ k }}</span
        >
      </p>
      <div v-if="findings.length" class="pr-finds" data-test="findings">
        <p class="pc-lbl">Findings</p>
        <ul>
          <li v-for="d in findings" :key="d.id">
            <button
              type="button"
              class="pr-find"
              :class="d.status"
              :data-test="`finding-${d.id}`"
              :title="d.inline ? `${d.path}:${d.line}` : 'General'"
              @click="goTo(d)"
            >
              <span v-if="d.run_id === ''" class="dc-you">You</span>
              <span v-else class="dc-sev" :data-sev="d.severity">{{
                d.severity
              }}</span>
              <span class="pr-find-t">{{
                d.run_id === "" ? d.body : d.title
              }}</span>
              <span class="pr-find-at mono"
                >{{ where(d)
                }}<template v-if="d.status !== 'pending'">
                  · {{ d.status }}</template
                ></span
              >
            </button>
          </li>
        </ul>
      </div>
      <label class="pc-lbl" for="pr-summary">Summary</label>
      <textarea
        id="pr-summary"
        v-model="summary"
        class="pr-summary"
        rows="8"
        data-test="review-summary"
        @input="touched = true"
        @blur="saveSummary"
      ></textarea>
      <fieldset class="pr-verdict">
        <legend class="pc-lbl">Verdict</legend>
        <label
          v-for="o in verdictOpts"
          :key="o.key"
          :class="{ off: !o.ok }"
          :title="o.ok ? undefined : o.reason"
        >
          <input
            type="radio"
            name="pr-verdict"
            :value="o.key"
            :checked="verdict === o.key"
            :disabled="!o.ok"
            :data-test="`verdict-${o.key}`"
            @change="setVerdict(o.key)"
          />
          {{ o.label }}
          <small v-if="!o.ok" class="muted" data-test="verdict-reason">{{
            o.reason
          }}</small>
        </label>
      </fieldset>
      <p class="small muted" data-test="accepted-count">
        {{ count }} accepted comment{{ count === 1 ? "" : "s" }} ·
        {{ store.pending.length }} to decide
      </p>
      <p v-if="postedHere" class="small" data-test="review-posted">
        Posted as {{ store.review?.posted_as }} ·
        {{ verdictLabel(store.review?.posted_verdict ?? "") }}
      </p>
      <div
        v-if="store.posted"
        class="pr-result"
        role="status"
        data-test="post-result"
      >
        <p class="small">
          Posted {{ Object.keys(store.posted.comments ?? {}).length }}
          comment(s).
        </p>
        <p v-if="store.posted.verdict_err" class="small prv-bad">
          Verdict not applied: {{ store.posted.verdict_err }}
        </p>
        <ul
          v-if="Object.keys(store.posted.failed ?? {}).length"
          class="pr-fail"
        >
          <li v-for="(msg, id) in store.posted.failed" :key="id">
            <strong>{{ titleOf(String(id)) }}</strong>: {{ msg }}
          </li>
        </ul>
      </div>
      <button
        type="button"
        class="lg-btn primary"
        data-test="post-open"
        :disabled="!canPost || store.posting"
        :title="detail?.identity ? undefined : 'Connect a review account first'"
        @click="openConfirm"
      >
        {{
          failedCount
            ? `Retry ${failedCount} failed…`
            : `Post review… · ${count} comment${count === 1 ? "" : "s"}`
        }}
      </button>
    </PanelCard>
    <AppDialog
      :open="confirming"
      title="Post this review?"
      @close="confirming = false"
    >
      <div v-if="expect && detail?.identity" data-test="post-dialog">
        <p
          v-if="conflict"
          class="notice warn"
          role="alert"
          data-test="post-conflict"
        >
          {{ conflict.error }} It now reads: account
          <strong>{{ conflict.current.login }}</strong>, head
          <span class="mono">{{ conflict.current.head_sha.slice(0, 7) }}</span>,
          {{ conflict.current.comments }} comment(s), verdict
          {{ verdictLabel(conflict.current.verdict) }}. Confirm again to post
          with these values.
        </p>
        <p>
          Post as <strong>{{ expect.login }}</strong> on
          {{ detail.identity.host }} to
          {{ store.review?.repo || detail.identity.host }}#{{
            store.pr.number
          }}:
          <strong data-test="post-comments">{{ expect.comments }}</strong>
          inline comment(s), summary, verdict
          <strong>{{ verdictLabel(expect.verdict) }}</strong>.
        </p>
        <p class="small muted">
          Public on {{ detail.identity.host }}: everyone with access to this
          pull request will see it under your name.
        </p>
        <p v-if="postError" class="prv-bad" role="alert">{{ postError }}</p>
        <div class="row">
          <button type="button" class="lg-btn" @click="confirming = false">
            Cancel
          </button>
          <button
            type="button"
            class="lg-btn primary"
            data-test="post-confirm"
            :disabled="store.posting"
            @click="confirmPost"
          >
            {{ conflict ? "Confirm and post" : "Post" }}
          </button>
        </div>
      </div>
    </AppDialog>
  </template>
</template>
