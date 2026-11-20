import { defineStore } from "pinia";
import { onBeforeUnmount, watch } from "vue";
import { api, ApiError } from "../types/api";
import { useFactory } from "./factory";
import type { LiveEvent } from "../services/events/sse";
import type {
  PostedReview,
  PostExpect,
  PRDetailResponse,
  PRDraft,
  PRFile,
  PRFilter,
  PullsResponse,
  InboxGroup,
  PRReview,
  PRReviewRun,
} from "../types/review";

export type PostConflict = { error: string; current: PostExpect };

const base = (project: string, n: number) =>
  `/api/v1/projects/${encodeURIComponent(project)}/pull-requests/${n}`;

export const fetchPulls = (project: string, filter: PRFilter) =>
  api.get<PullsResponse>(
    `/api/v1/projects/${encodeURIComponent(project)}/pull-requests?filter=${filter}`,
  );

export const fetchInbox = (filter: PRFilter) =>
  api.get<InboxGroup[]>(`/api/v1/reviews?filter=${filter}`);

export const isPRReviewEvent = (ev: LiveEvent) =>
  typeof ev.kind === "string" && ev.kind.startsWith("pr_review.");

export const eventTarget = (ev: LiveEvent) => {
  const p = (ev.payload ?? {}) as { project_id?: string; number?: number };
  return { project: p.project_id ?? "", number: p.number ?? 0 };
};

// Reuses the factory store's single event stream; refreshes on pr_review.* events, every 60 s while visible, and on focus.
export function usePRRefresh(
  refresh: () => void,
  match: (ev: LiveEvent) => boolean = () => true,
  every = 60_000,
) {
  const factory = useFactory();
  const visible = () => typeof document === "undefined" || !document.hidden;
  const timer = setInterval(() => visible() && refresh(), every);
  const onShow = () => visible() && refresh();
  window.addEventListener("focus", onShow);
  document.addEventListener("visibilitychange", onShow);
  const stop = watch(
    () => factory.liveEvents[0],
    (ev) => {
      if (ev && isPRReviewEvent(ev) && match(ev)) refresh();
    },
  );
  onBeforeUnmount(() => {
    clearInterval(timer);
    window.removeEventListener("focus", onShow);
    document.removeEventListener("visibilitychange", onShow);
    stop();
  });
}

async function postReview(
  project: string,
  n: number,
  expect: PostExpect,
): Promise<PostedReview> {
  const resp = await fetch(`${base(project, n)}/review/post`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ confirm: true, expect }),
  });
  const text = await resp.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (resp.ok) return data as PostedReview;
  const body = (data ?? {}) as {
    error?: { code?: string; message?: string } | string;
    current?: PostExpect;
  };
  const message =
    typeof body.error === "string"
      ? body.error
      : (body.error?.message ?? text.slice(0, 200));
  if (resp.status === 409 && body.current)
    throw Object.assign(new Error(message), {
      conflict: { error: message, current: body.current } as PostConflict,
    });
  throw new ApiError(
    resp.status,
    typeof body.error === "object"
      ? (body.error?.code ?? "HTTP_ERROR")
      : "HTTP_ERROR",
    message,
  );
}

export const useReviews = defineStore("reviews", {
  state: () => ({
    projectId: "",
    number: 0,
    detail: null as PRDetailResponse | null,
    files: [] as PRFile[],
    filesError: "",
    drafts: [] as PRDraft[],
    viewed: { viewed: 0, total: 0 },
    loading: false,
    error: "",
    notFound: false,
    posting: false,
    posted: null as PostedReview | null,
    seq: 0,
    // File at the top of the diff while scrolling; highlights the tree row.
    current: "",
    // Bumped to re-run a jump the URL already holds (same file or finding clicked again).
    focusSeq: 0,
  }),
  getters: {
    pr: (s) => s.detail?.pr ?? null,
    review: (s): PRReview | null => s.detail?.review ?? null,
    run: (s): PRReviewRun | null => s.detail?.run ?? null,
    running: (s) => s.detail?.run?.state === "running",
    live: (s) =>
      s.drafts.filter(
        (d) => d.status !== "discarded" && d.status !== "superseded",
      ),
    accepted: (s) => s.drafts.filter((d) => d.status === "accepted"),
    pending: (s) => s.drafts.filter((d) => d.status === "pending"),
    general: (s) =>
      s.drafts.filter((d) => !d.inline && d.status !== "superseded"),
    viewedCount: (s) => s.files.filter((f) => f.viewed).length,
    stale: (s) =>
      !!s.detail?.review?.head_sha &&
      !!s.detail.pr.head_sha &&
      s.detail.review.head_sha !== s.detail.pr.head_sha,
  },
  actions: {
    async load(project: string, n: number, silent = false) {
      const switched = project !== this.projectId || n !== this.number;
      if (switched) {
        this.$reset();
        this.projectId = project;
        this.number = n;
      }
      const mine = ++this.seq;
      if (!silent) this.loading = true;
      try {
        const detail = await api.get<PRDetailResponse>(base(project, n));
        const [files, drafts] = await Promise.all([
          api
            .get<PRFile[]>(`${base(project, n)}/files`)
            .then((f) => ({ f, err: "" }))
            .catch((e: unknown) => ({
              f: null,
              err: e instanceof Error ? e.message : String(e),
            })),
          api.get<PRDraft[]>(`${base(project, n)}/review/drafts`),
        ]);
        if (mine !== this.seq) return;
        this.detail = detail;
        if (files.f) this.files = files.f ?? [];
        this.filesError = files.err;
        this.drafts = drafts ?? [];
        this.viewed = {
          viewed: this.viewedCount,
          total: this.files.length,
        };
        this.error = "";
        this.notFound = false;
      } catch (e) {
        if (mine !== this.seq) return;
        this.notFound = e instanceof ApiError && e.status === 404;
        this.error = e instanceof Error ? e.message : String(e);
      } finally {
        if (mine === this.seq) this.loading = false;
      }
    },
    refresh() {
      return this.projectId
        ? this.load(this.projectId, this.number, true)
        : Promise.resolve();
    },
    async runAI() {
      const run = await api.post<PRReviewRun>(
        `${base(this.projectId, this.number)}/review/runs`,
      );
      if (this.detail) this.detail = { ...this.detail, run };
    },
    async addComment(c: {
      path: string;
      side: "new" | "old";
      line: number;
      body: string;
    }) {
      const d = await api.post<PRDraft>(
        `${base(this.projectId, this.number)}/review/drafts`,
        c,
      );
      this.drafts = [...this.drafts, d];
    },
    async patchDraft(id: string, patch: { status?: string; body?: string }) {
      const d = await api.patch<PRDraft>(
        `${base(this.projectId, this.number)}/review/drafts/${encodeURIComponent(id)}`,
        patch,
      );
      this.drafts = this.drafts.map((x) => (x.id === id ? d : x));
    },
    async setViewed(path: string, viewed: boolean) {
      const prev = this.files;
      this.files = prev.map((f) => (f.path === path ? { ...f, viewed } : f));
      try {
        this.viewed = await api.put<{ viewed: number; total: number }>(
          `${base(this.projectId, this.number)}/review/viewed`,
          { path, viewed },
        );
      } catch (e) {
        this.files = prev;
        throw e;
      }
    },
    async saveReview(patch: { summary?: string; verdict?: string }) {
      const review = await api.patch<PRReview>(
        `${base(this.projectId, this.number)}/review`,
        patch,
      );
      if (this.detail) this.detail = { ...this.detail, review };
    },
    async post(expect: PostExpect) {
      this.posting = true;
      try {
        this.posted = await postReview(this.projectId, this.number, expect);
      } finally {
        this.posting = false;
      }
      await this.refresh();
    },
  },
});
