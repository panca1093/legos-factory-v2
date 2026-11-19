import { ApiError } from "../../types/api";
import type {
  EditorFolder,
  FileContentDTO,
  FileStatDTO,
  TreeEntryDTO,
} from "./types";

export class FsError extends ApiError {
  constructor(
    status: number,
    code: string,
    message: string,
    public body: unknown,
  ) {
    super(status, code, message);
  }
}

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
  });
  const text = await resp.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!resp.ok) {
    if (resp.status === 401 && typeof window !== "undefined")
      window.dispatchEvent(new Event("factory:unauthorized"));
    const err = (data as { error?: { code?: string; message?: string } })
      ?.error;
    throw new FsError(
      resp.status,
      err?.code ?? "HTTP_ERROR",
      err?.message ?? text.slice(0, 200),
      data,
    );
  }
  return data as T;
}

const base = (fid: string) =>
  `/api/v1/editor/folders/${encodeURIComponent(fid)}`;
const q = (rel: string) => encodeURIComponent(rel);

export interface Conflict {
  current_hash: string;
  mtime: string;
  actor: unknown;
}

// The 409 body carries the current disk stat; the envelope may nest it under error.
export function conflictOf(e: unknown): Conflict | null {
  if (!(e instanceof FsError) || e.status !== 409) return null;
  const b = (e.body ?? {}) as Record<string, unknown>;
  const err = (b.error ?? {}) as Record<string, unknown>;
  const src = (
    b.current_hash ? b : (err.details ?? (err.current_hash ? err : b))
  ) as Partial<Conflict>;
  return {
    current_hash: src.current_hash ?? "",
    mtime: src.mtime ?? "",
    actor: src.actor ?? null,
  };
}

export const fsApi = {
  projectFolder: (projectID: string) =>
    call<{ folder?: EditorFolder; needs_folder?: boolean }>(
      "GET",
      `/api/v1/projects/${encodeURIComponent(projectID)}/editor`,
    ),
  openWorktree: (wo_id: string, task_id?: string) =>
    call<{ folder: EditorFolder }>("POST", "/api/v1/editor/folders", {
      wo_id,
      ...(task_id ? { task_id } : {}),
    }),
  tree: (fid: string, dir: string) =>
    call<{ entries: TreeEntryDTO[]; truncated?: number | boolean }>(
      "GET",
      `${base(fid)}/tree?path=${q(dir || ".")}`,
    ),
  files: async (fid: string): Promise<string[]> => {
    const r = await call<string[] | { paths?: string[]; files?: string[] }>(
      "GET",
      `${base(fid)}/files`,
    );
    return Array.isArray(r) ? r : (r.paths ?? r.files ?? []);
  },
  read: (fid: string, path: string) =>
    call<FileContentDTO>("GET", `${base(fid)}/file?path=${q(path)}`),
  write: (
    fid: string,
    body: {
      path: string;
      content: string;
      base_hash: string;
      create?: boolean;
    },
  ) => call<FileStatDTO>("PUT", `${base(fid)}/file`, body),
  mkdir: (fid: string, path: string) =>
    call<unknown>("POST", `${base(fid)}/mkdir`, { path }),
  rename: (fid: string, from: string, to: string) =>
    call<unknown>("POST", `${base(fid)}/rename`, { from, to }),
};
