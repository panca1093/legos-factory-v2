// Wire shapes of the Review PR API (docs/review-pr/CONTRACT.md).

export type HostUser = { login: string; id: string };

export type Identity = {
  provider: string;
  host: string;
  source: "connection" | "gh" | "glab";
  login: string;
  readiness: "ready" | "read_only" | "unknown" | "expired" | "invalid";
  expires_at?: string;
} | null;

export type ProviderErr = {
  code: "no_identity" | "rate_limited" | "provider" | "not_found" | "forbidden";
  message: string;
  until?: string;
};

export type PRLabel = "ai" | "yours" | "new" | "approve" | "none";
export type PRFilter = "related" | "reviewer" | "author";

export type PRRow = {
  number: number;
  title: string;
  url: string;
  author: HostUser;
  head: string;
  base: string;
  draft: boolean;
  updated_at: string;
  role: "author" | "reviewer" | "both";
  label: PRLabel;
  pending_drafts: number;
  running: boolean;
  factory_wo_id?: string;
};

export type PullsResponse = {
  identity: Identity;
  rows: PRRow[];
  stale: boolean;
  error?: ProviderErr;
};

export type InboxGroup = {
  project: { id: string; name: string };
  identity: Identity;
  rows: PRRow[];
  error?: ProviderErr;
};

export type RailPull = {
  number: number;
  title: string;
  label: PRLabel;
  pending_drafts: number;
  running: boolean;
};

export type RailPulls = {
  identity: Identity;
  count: number;
  rows: RailPull[];
  stale: boolean;
  error?: ProviderErr | string;
};

export type PRDetail = {
  number: number;
  title: string;
  url: string;
  author: HostUser;
  head: string;
  base: string;
  head_sha: string;
  base_sha: string;
  draft: boolean;
  updated_at: string;
  body: string;
  additions: number;
  deletions: number;
  changed_files: number;
  ci: "passing" | "failing" | "running" | "none";
  source_repo: string;
  my_latest_review?: { verdict: string; commit_id: string };
};

export type PRFile = {
  path: string;
  old_path: string;
  status: "added" | "modified" | "deleted" | "renamed";
  additions: number;
  deletions: number;
  patch: string;
  truncated: boolean;
  viewed: boolean;
};

export type Verdict = "comment" | "approve" | "request_changes";

export type PRReview = {
  id: string;
  head_sha: string;
  summary: string;
  verdict: string;
  repo: string;
  posted_at?: string;
  posted_head_sha: string;
  posted_verdict: string;
  posted_as: string;
};

export type PRReviewRun = {
  id: string;
  head_sha: string;
  state: "running" | "completed" | "failed";
  summary: string;
  error: string;
  started_at: string;
  finished_at?: string;
};

export type Capability = { ok: boolean; reason?: string };

export type PRDetailResponse = {
  pr: PRDetail;
  identity: Identity;
  review: PRReview | null;
  run: PRReviewRun | null;
  verdicts: { approve: Capability; request_changes: Capability };
  ai: Capability;
};

export type DraftStatus =
  "pending" | "accepted" | "discarded" | "superseded" | "outdated" | "posted";

export type PRDraft = {
  id: string;
  run_id: string; // "" = the user's own comment
  path: string;
  old_path: string;
  side: "new" | "old";
  line: number;
  old_line: number;
  inline: boolean;
  severity: "blocker" | "major" | "minor" | "info";
  title: string;
  body: string;
  ai_body: string;
  status: DraftStatus;
  posted_ref: string;
  post_error: string;
};

export type PostExpect = {
  login: string;
  head_sha: string;
  comments: number;
  verdict: string;
};

export type PostedReview = {
  ref: string;
  comments: Record<string, string>;
  failed: Record<string, string>;
  verdict_err?: string;
};

export function errCode(e: ProviderErr | string | undefined): string {
  return typeof e === "string" ? e : (e?.code ?? "");
}

export function errMessage(e: ProviderErr | string | undefined): string {
  if (!e) return "";
  if (typeof e !== "string") return e.message;
  const known: Record<string, string> = {
    no_identity: "No review account is connected.",
    rate_limited: "The host is rate limiting requests.",
  };
  return known[e] ?? e;
}

export function chipText(r: {
  label: PRLabel;
  pending_drafts: number;
  running: boolean;
}): string {
  if (r.running) return "AI · …";
  switch (r.label) {
    case "ai":
      return `AI · ${r.pending_drafts}`;
    case "none":
      return "";
    default:
      return r.label;
  }
}

export function statusLine(r: PRRow): string {
  if (r.running) return "AI reviewing…";
  switch (r.label) {
    case "ai":
      return `AI draft ready · ${r.pending_drafts}`;
    case "approve":
      return "Needs your approval";
    case "yours":
      return "Your pull request";
    case "none":
      return "Posted";
    default:
      return "Not reviewed";
  }
}

export const prFilter = (q: unknown): PRFilter =>
  q === "reviewer" || q === "author" ? q : "related";

export const pullPath = (projectID: string, n: number) =>
  `/projects/${projectID}/pulls/${n}`;

export const connectionsPath = "/settings?section=connections";
