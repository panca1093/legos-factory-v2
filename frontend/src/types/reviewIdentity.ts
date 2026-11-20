export type Readiness =
  "ready" | "read_only" | "unknown" | "expired" | "invalid";

export interface ReviewIdentity {
  provider: string;
  host: string;
  source: "connection" | "gh" | "glab" | "workbench";
  connection_id?: string;
  login: string;
  readiness: Readiness;
  expires_at?: string;
}

export interface IdentityConnection {
  id: string;
  name: string;
  login: string;
  readiness: Readiness;
  expires_at?: string;
}

export interface IdentityWorkbench {
  id: string;
  name: string;
}

export interface IdentityGroup {
  provider: string;
  host: string;
  pinned: ReviewIdentity | null;
  connections: IdentityConnection[];
  cli: { tool: "gh"; installed: boolean; users: string[] } | null;
  workbench?: IdentityWorkbench[];
}

export interface CLILoginStep {
  login_id: string;
  kind: "code" | "url" | "done" | "failed" | "fallback";
  code?: string;
  url?: string;
  login?: string;
  message?: string;
}

export interface ProjectSummary {
  project_id: string;
  needs_you: number;
  wo_running: number;
  prs_for_me: number | null;
  shipped_30d: number;
  last_activity: string;
  identity: ReviewIdentity | null;
}

const DAY = 86_400_000;

export function readinessLabel(
  readiness: Readiness,
  expiresAt?: string,
  now = Date.now(),
): { tone: "green" | "orange" | "red"; text: string } {
  if (readiness === "expired") return { tone: "red", text: "Expired" };
  if (readiness === "invalid") return { tone: "red", text: "Invalid" };
  if (expiresAt) {
    const days = Math.ceil((Date.parse(expiresAt) - now) / DAY);
    if (days <= 0) return { tone: "red", text: "Expired" };
    if (days <= 7)
      return {
        tone: "orange",
        text: `Expires in ${days} day${days === 1 ? "" : "s"}`,
      };
  }
  if (readiness === "read_only")
    return { tone: "orange", text: "Read only, cannot post" };
  if (readiness === "unknown")
    return { tone: "orange", text: "Unknown, write not probed" };
  return { tone: "green", text: "Ready" };
}
