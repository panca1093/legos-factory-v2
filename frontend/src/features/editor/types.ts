export interface EditorFolder {
  id: string;
  path: string;
  kind: "project" | "worktree" | "folder";
  project_id?: string;
  wo_id?: string;
  task_id?: string;
  branch?: string;
  opened_at?: string;
}

export interface TreeEntryDTO {
  name: string;
  kind: "file" | "dir" | "symlink";
  size: number;
  mtime: string;
  target_inside?: boolean;
  ignored: boolean;
}

export interface FileContentDTO {
  content: string;
  hash: string;
  mtime: string;
  size: number;
  eol: "LF" | "CRLF";
  bom: boolean;
  readonly: boolean;
}

export interface FileStatDTO {
  hash: string;
  mtime: string;
  size?: number;
}

export interface Actor {
  role: string;
  wo_id?: string;
  task_id?: string;
  label: string;
}

export interface GitEntry {
  path: string;
  x: string;
  y: string;
}

export interface GitFrame {
  repo?: boolean;
  branch: string;
  ahead: number;
  behind: number;
  entries: GitEntry[];
  too_many: boolean;
}

export type ControlFrame =
  | {
      type: "file_changed";
      path: string;
      kind: "changed" | "deleted" | "created";
      hash: string;
      mtime: string;
      actor: Actor | null;
    }
  | { type: "dir_changed"; dir: string }
  | ({ type: "git_status" } & GitFrame)
  | { type: "folder_gone" };

export interface ChangeInfo {
  kind: "changed" | "deleted";
  hash: string;
  mtime: string;
  actor: Actor | null;
}

export interface PersistedTab {
  path: string;
  preview: boolean;
  cursor?: { line: number; col: number };
  scroll?: number;
}

export interface PersistedFolder {
  v: 1;
  project_id: string;
  folder: EditorFolder;
  tabs: PersistedTab[];
  active: string;
}
