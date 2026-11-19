import { readStored, writeStored } from "../projects/rail";
import type { PersistedFolder } from "./types";

const PREFIX = "legos-editor:";
export const storageKey = (userID: string, folderID: string) =>
  `${PREFIX}${userID}:${folderID}`;

function parse(raw: string | null): PersistedFolder | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as PersistedFolder;
    if (v?.v !== 1 || !v.folder?.id || !Array.isArray(v.tabs)) return null;
    return v;
  } catch {
    return null;
  }
}

export function readFolder(
  userID: string,
  folderID: string,
): PersistedFolder | null {
  return parse(readStored(storageKey(userID, folderID)));
}

export function writeFolder(userID: string, p: PersistedFolder) {
  writeStored(storageKey(userID, p.folder.id), JSON.stringify(p));
}

export function dropFolder(userID: string, folderID: string) {
  try {
    localStorage.removeItem(storageKey(userID, folderID));
  } catch {
    /* Browser storage can be disabled. */
  }
}

// Reads without Monaco or the store, so the rail can list open files on any selection.
export function readProjectFolders(
  userID: string,
  projectID: string,
): PersistedFolder[] {
  const out: PersistedFolder[] = [];
  try {
    const prefix = `${PREFIX}${userID}:`;
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (!k?.startsWith(prefix)) continue;
      const v = parse(localStorage.getItem(k));
      if (v && v.project_id === projectID && v.tabs.length) out.push(v);
    }
  } catch {
    return out;
  }
  return out.sort(
    (a, b) =>
      Number(a.folder.kind !== "project") - Number(b.folder.kind !== "project"),
  );
}
