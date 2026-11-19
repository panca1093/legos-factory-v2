import { defineStore } from "pinia";
import { computed, markRaw, reactive, ref, shallowRef } from "vue";
import { useSession } from "../../stores/session";
import { useToast } from "../../composables/useToast";
import {
  rollupCounts,
  rollupMarkers,
  type Markers,
} from "../../components/files/tree";
import { buildWatchSet, ControlSocket } from "./control";
import { conflictOf, FsError, fsApi } from "./fs";
import { languageOf, loadMonaco, type ITextModel, type Monaco } from "./monaco";
import {
  dropFolder,
  readFolder,
  readProjectFolders,
  writeFolder,
} from "./openFiles";
import type {
  Actor,
  ControlFrame,
  Diagnostic,
  EditorFolder,
  GitFrame,
  PersistedTab,
  ServerState,
  TreeEntryDTO,
} from "./types";

export interface Banner {
  kind: "changed" | "deleted" | "conflict";
  hash: string;
  mtime: string;
  actor: Actor | null;
  comparing: boolean;
}

export interface Buffer {
  path: string;
  status: "loading" | "ready" | "placeholder" | "error";
  language: string;
  baseHash: string;
  mtime: string;
  size: number;
  eol: "LF" | "CRLF";
  bom: boolean;
  readonly: boolean;
  dirty: boolean;
  saving: boolean;
  saveError: string;
  lastSaved: string;
  recreate: boolean;
  banner: Banner | null;
  placeholder: { code: number; size?: number } | null;
  error: string;
}

export interface FolderState {
  folder: EditorFolder;
  status: "dormant" | "loading" | "ready" | "gone" | "error";
  error: string;
  tabs: PersistedTab[];
  active: string;
  buffers: Record<string, Buffer>;
  listings: Record<string, TreeEntryDTO[]>;
  truncated: Record<string, number>;
  expanded: string[];
  git: GitFrame | null;
  markers: Markers | null;
  connected: boolean;
  quick: string[] | null;
}

export interface ProjectState {
  status: "loading" | "ready" | "unavailable" | "needs_folder" | "error";
  message: string;
  folderId: string;
}

export interface ModelInfo {
  uri: string;
  path: string;
  folderId: string;
  language: string;
  model: ITextModel;
}

export interface RevealTarget {
  folderId: string;
  path: string;
  line: number;
  col: number;
  n: number;
}

export type CloseChoice = "save" | "discard" | "cancel";
type Disposable = { dispose(): void };

const newBuffer = (path: string): Buffer => ({
  path,
  status: "loading",
  language: languageOf(path),
  baseHash: "",
  mtime: "",
  size: 0,
  eol: "LF",
  bom: false,
  readonly: false,
  dirty: false,
  saving: false,
  saveError: "",
  lastSaved: "",
  recreate: false,
  banner: null,
  placeholder: null,
  error: "",
});

const newFolder = (folder: EditorFolder): FolderState => ({
  folder,
  status: "loading",
  error: "",
  tabs: [],
  active: "",
  buffers: {},
  listings: {},
  truncated: {},
  expanded: [],
  git: null,
  markers: null,
  connected: false,
  quick: null,
});

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));
const dirOf = (p: string) =>
  p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "";
const join = (d: string, n: string) => (d ? `${d}/${n}` : n);

export const useEditor = defineStore("editor", () => {
  const session = useSession();
  const { toast } = useToast();

  const folders = reactive<Record<string, FolderState>>({});
  const projects = reactive<Record<string, ProjectState>>({});
  const workspaceProject = ref("");
  const currentId = ref("");
  const monaco = shallowRef<Monaco | null>(null);
  const models = markRaw(new Map<string, ITextModel>());
  const diagnostics = reactive(new Map<string, Diagnostic[]>());
  const serverStates = reactive<Record<string, ServerState>>({});
  const reveal = ref<RevealTarget | null>(null);
  const closeRequest = ref<{ folderId: string; paths: string[] } | null>(null);
  const leavePrompt = ref<{ resolve: (ok: boolean) => void } | null>(null);
  const quickOpen = ref(false);

  const versions = new Map<string, number>();
  const subs = new Map<string, Disposable>();
  const sockets = new Map<string, ControlSocket>();
  const opened = new Set<(i: ModelInfo) => void>();
  const closed = new Set<(i: ModelInfo) => void>();
  const saved = new Set<(uri: string) => void>();
  const watchTimers = new Map<string, ReturnType<typeof setTimeout>>();
  const persistTimers = new Map<string, ReturnType<typeof setTimeout>>();
  let revealSeq = 0;
  let attached = false;

  const userId = () => session.user?.id ?? "";
  const current = () => folders[currentId.value];
  // The language client's view of the open folder (lsp.ts reads `folder`).
  const folder = computed(() => {
    const f = current()?.folder;
    return f ? { id: f.id, path: f.path } : null;
  });

  function infoOf(fid: string, path: string, model: ITextModel): ModelInfo {
    return {
      uri: model.uri.toString(),
      path,
      folderId: fid,
      language: folders[fid]?.buffers[path]?.language ?? languageOf(path),
      model,
    };
  }

  function onModelOpened(cb: (i: ModelInfo) => void) {
    opened.add(cb);
    for (const [fid, st] of Object.entries(folders))
      for (const [path, b] of Object.entries(st.buffers)) {
        const m = b.status === "ready" ? modelOf(fid, path) : undefined;
        if (m) cb(infoOf(fid, path, m));
      }
    return () => opened.delete(cb);
  }
  function onModelClosed(cb: (i: ModelInfo) => void) {
    closed.add(cb);
    return () => closed.delete(cb);
  }

  function onModelSaved(cb: (uri: string) => void) {
    saved.add(cb);
    return () => saved.delete(cb);
  }

  // 1-based line and column; a uri outside every open folder is ignored.
  async function openFileAt(
    uri: string,
    pos: { line?: number; column?: number } = {},
  ) {
    const loc = locate(uri);
    if (!loc) return;
    currentId.value = loc.folderId;
    await openFile(loc.folderId, loc.path, {
      line: pos.line ?? 1,
      col: pos.column ?? 1,
    });
  }

  // Opens a file of an open folder as a (background) model so workspace edits can touch it.
  async function ensureModel(uri: string): Promise<ITextModel | null> {
    const existing = models.get(uri);
    if (existing) return existing;
    const loc = locate(uri);
    if (!loc) return null;
    const st = folders[loc.folderId];
    if (!st || st.status !== "ready") return null;
    if (!st.tabs.some((t) => t.path === loc.path)) {
      st.tabs.push({ path: loc.path, preview: false });
      persist(loc.folderId);
      syncWatch(loc.folderId);
    }
    await ensureBuffer(loc.folderId, loc.path);
    return modelOf(loc.folderId, loc.path) ?? null;
  }

  function uriOf(fid: string, path: string): string {
    const st = folders[fid];
    const m = monaco.value;
    if (!st || !m) return "";
    return m.Uri.file(join(st.folder.path, path)).toString();
  }
  function modelOf(fid: string, path: string) {
    return models.get(uriOf(fid, path));
  }
  // Inverse of uriOf for a model URI inside a known folder.
  function locate(uri: string): { folderId: string; path: string } | null {
    let abs = uri;
    try {
      abs = decodeURIComponent(uri.replace(/^file:\/\//, ""));
    } catch {
      return null;
    }
    for (const [fid, st] of Object.entries(folders)) {
      const root = st.folder.path.replace(/\/$/, "") + "/";
      if (abs.startsWith(root))
        return { folderId: fid, path: abs.slice(root.length) };
    }
    return null;
  }

  // ---- persistence -------------------------------------------------------

  function persist(fid: string) {
    clearTimeout(persistTimers.get(fid));
    persistTimers.set(
      fid,
      setTimeout(() => {
        const st = folders[fid];
        if (!st || st.status === "gone") return;
        writeFolder(userId(), {
          v: 1,
          project_id: st.folder.project_id || workspaceProject.value,
          folder: st.folder,
          tabs: st.tabs.map((t) => ({ ...t })),
          active: st.active,
        });
      }, 150),
    );
  }
  function persistNow(fid: string) {
    const st = folders[fid];
    if (!st || st.status === "gone") return;
    writeFolder(userId(), {
      v: 1,
      project_id: st.folder.project_id || workspaceProject.value,
      folder: st.folder,
      tabs: st.tabs.map((t) => ({ ...t })),
      active: st.active,
    });
  }

  function hydrate(projectId: string) {
    for (const p of readProjectFolders(userId(), projectId)) {
      if (folders[p.folder.id]) continue;
      const st = newFolder(p.folder);
      st.status = "dormant";
      st.tabs = p.tabs;
      st.active = p.active;
      folders[p.folder.id] = st;
    }
  }

  function attach(projectId: string) {
    if (workspaceProject.value && workspaceProject.value !== projectId)
      detach();
    workspaceProject.value = projectId;
    attached = true;
    hydrate(projectId);
  }

  function detach() {
    for (const s of sockets.values()) s.close();
    sockets.clear();
    for (const t of watchTimers.values()) clearTimeout(t);
    for (const t of persistTimers.values()) clearTimeout(t);
    watchTimers.clear();
    persistTimers.clear();
    for (const uri of [...models.keys()]) disposeModel(uri);
    for (const k of Object.keys(folders)) delete folders[k];
    for (const k of Object.keys(projects)) delete projects[k];
    diagnostics.clear();
    currentId.value = "";
    workspaceProject.value = "";
    reveal.value = null;
    closeRequest.value = null;
    attached = false;
  }

  // ---- folders -----------------------------------------------------------

  function dormantOrNew(folder: EditorFolder): FolderState {
    let st = folders[folder.id];
    if (!st) {
      st = newFolder(folder);
      folders[folder.id] = st;
    } else {
      st.folder = { ...st.folder, ...folder };
    }
    return st;
  }

  async function ensureFolder(folder: EditorFolder) {
    const st = dormantOrNew(folder);
    const first = st.status !== "ready";
    st.status = "ready";
    st.error = "";
    if (first) {
      connectControl(folder.id);
      await loadDir(folder.id, "");
      void restoreTabs(folder.id);
    }
  }

  async function openProject(projectId: string): Promise<string> {
    if (!attached || workspaceProject.value !== projectId) attach(projectId);
    const known = projects[projectId];
    if (
      known?.status === "ready" &&
      folders[known.folderId]?.status === "ready"
    )
      return known.folderId;
    projects[projectId] = { status: "loading", message: "", folderId: "" };
    try {
      const r = await fsApi.projectFolder(projectId);
      if (!r.folder) {
        projects[projectId] = {
          status: "needs_folder",
          message: "This project has no local folder.",
          folderId: "",
        };
        return "";
      }
      await ensureFolder({
        ...r.folder,
        project_id: r.folder.project_id || projectId,
      });
      projects[projectId] = {
        status: "ready",
        message: "",
        folderId: r.folder.id,
      };
      return r.folder.id;
    } catch (e) {
      const unavailable =
        e instanceof FsError &&
        (e.status === 404 || e.code === "NOT_AVAILABLE");
      projects[projectId] = {
        status: unavailable ? "unavailable" : "error",
        message: unavailable
          ? "The editor is only available when Legos runs locally in dev mode."
          : msg(e),
        folderId: "",
      };
      return "";
    }
  }

  // A worktree folder opened by id (route reload): the stored snapshot says which work order to resolve again.
  async function useFolder(projectId: string, fid: string): Promise<boolean> {
    if (!fid) {
      const pf = await openProject(projectId);
      currentId.value = pf;
      return !!pf;
    }
    if (!attached || workspaceProject.value !== projectId) attach(projectId);
    let st = folders[fid];
    if (!st) {
      const p = readFolder(userId(), fid);
      if (p) {
        hydrate(projectId);
        st = folders[fid];
      }
    }
    if (!st) return false;
    if (st.status !== "ready") {
      const f = st.folder;
      try {
        const r = f.wo_id
          ? await fsApi.openWorktree(f.wo_id, f.task_id)
          : { folder: f };
        await ensureFolder({
          ...r.folder,
          project_id: r.folder.project_id || projectId,
        });
        if (r.folder.id !== fid) {
          // The server minted a new id for the same path: move the dormant tabs over.
          const fresh = folders[r.folder.id];
          fresh.tabs = st.tabs;
          fresh.active = st.active;
          dropFolder(userId(), fid);
          delete folders[fid];
          fid = r.folder.id;
          persistNow(fid);
        }
      } catch (e) {
        if (isGone(e)) markGone(fid);
        else {
          st.status = "error";
          st.error = msg(e);
        }
        currentId.value = fid;
        return false;
      }
    }
    currentId.value = fid;
    return true;
  }

  async function openWorktree(
    woId: string,
    taskId?: string,
  ): Promise<EditorFolder> {
    const r = await fsApi.openWorktree(woId, taskId);
    const pid = r.folder.project_id || workspaceProject.value;
    if (pid && (!attached || workspaceProject.value !== pid)) attach(pid);
    await ensureFolder({ ...r.folder, project_id: pid });
    persistNow(r.folder.id);
    return { ...r.folder, project_id: pid };
  }

  const isGone = (e: unknown) =>
    e instanceof FsError && (e.code === "FOLDER_GONE" || e.status === 410);

  function markGone(fid: string) {
    const st = folders[fid];
    if (!st) return;
    st.status = "gone";
    dropFolder(userId(), fid);
    sockets.get(fid)?.close();
    sockets.delete(fid);
  }

  // ---- control socket ----------------------------------------------------

  function connectControl(fid: string) {
    if (sockets.has(fid)) return;
    const sock = new ControlSocket(fid, {
      frame: (f) => onFrame(fid, f),
      state: (s) => {
        const st = folders[fid];
        if (st) st.connected = s === "open";
      },
      open: (again) => {
        if (again) void recheck(fid);
      },
    });
    sockets.set(fid, sock);
    sock.connect();
    syncWatch(fid);
  }

  function syncWatch(fid: string) {
    clearTimeout(watchTimers.get(fid));
    watchTimers.set(
      fid,
      setTimeout(() => {
        const st = folders[fid];
        if (!st) return;
        sockets.get(fid)?.setWatch(
          buildWatchSet(
            st.tabs.map((t) => t.path),
            st.expanded,
          ),
        );
      }, 80),
    );
  }

  function onFrame(fid: string, f: ControlFrame) {
    const st = folders[fid];
    if (!st) return;
    switch (f.type) {
      case "file_changed":
        void onFileChanged(
          fid,
          f.path,
          f.kind,
          f.hash,
          f.mtime,
          f.actor ?? null,
        );
        break;
      case "dir_changed":
        st.quick = null;
        if (f.dir === "" || st.expanded.includes(f.dir))
          void loadDir(fid, f.dir);
        break;
      case "git_status":
        st.git = f;
        st.markers = f.too_many ? null : markRaw(rollupMarkers(f.entries));
        if (f.branch) st.folder = { ...st.folder, branch: f.branch };
        break;
      case "server_state":
        serverStates[f.server] = f;
        break;
      case "folder_gone":
        markGone(fid);
        break;
    }
  }

  async function onFileChanged(
    fid: string,
    path: string,
    kind: "changed" | "deleted" | "created",
    hash: string,
    mtime: string,
    actor: Actor | null,
  ) {
    const b = folders[fid]?.buffers[path];
    if (!b || b.status !== "ready") return;
    if (kind === "deleted") {
      b.banner = { kind: "deleted", hash: "", mtime, actor, comparing: false };
      return;
    }
    if (b.saving || hash === b.baseHash || hash === b.lastSaved) return;
    if (b.banner?.kind === "changed" && b.banner.hash === hash) return;
    if (!b.dirty && !b.banner) {
      await reloadBuffer(fid, path);
      toast(`Reloaded · changed by ${actor?.label ?? "another program"}`);
      return;
    }
    b.banner = {
      kind: "changed",
      hash,
      mtime,
      actor,
      comparing: b.banner?.comparing ?? false,
    };
  }

  // After a dropped socket the events in between are lost: compare every loaded buffer to the disk.
  async function recheck(fid: string) {
    const st = folders[fid];
    if (!st) return;
    void loadDir(fid, "");
    for (const d of st.expanded) void loadDir(fid, d);
    for (const [path, b] of Object.entries(st.buffers)) {
      if (b.status !== "ready") continue;
      try {
        const r = await fsApi.read(fid, path);
        await onFileChanged(fid, path, "changed", r.hash, r.mtime, null);
      } catch (e) {
        if (e instanceof FsError && e.status === 404)
          await onFileChanged(fid, path, "deleted", "", "", null);
      }
    }
  }

  // ---- tree --------------------------------------------------------------

  async function loadDir(fid: string, dir: string) {
    const st = folders[fid];
    if (!st) return;
    try {
      const r = await fsApi.tree(fid, dir);
      st.listings[dir] = r.entries;
      const t = typeof r.truncated === "number" ? r.truncated : 0;
      if (t) st.truncated[dir] = t;
      else delete st.truncated[dir];
      const kids = r.entries;
      if (
        kids.length === 1 &&
        kids[0].kind === "dir" &&
        !st.listings[join(dir, kids[0].name)]
      )
        void loadDir(fid, join(dir, kids[0].name));
    } catch (e) {
      if (isGone(e)) markGone(fid);
      else if (dir === "") {
        st.status = "error";
        st.error = msg(e);
      }
    }
  }

  async function toggleDir(fid: string, dir: string) {
    const st = folders[fid];
    if (!st) return;
    const i = st.expanded.indexOf(dir);
    if (i >= 0) st.expanded.splice(i, 1);
    else {
      st.expanded.push(dir);
      if (!st.listings[dir]) await loadDir(fid, dir);
    }
    syncWatch(fid);
  }

  async function revealInTree(fid: string, path: string) {
    const st = folders[fid];
    if (!st) return;
    const parts = path.split("/").slice(0, -1);
    let dir = "";
    for (const p of parts) {
      dir = join(dir, p);
      if (!st.expanded.includes(dir)) {
        st.expanded.push(dir);
        if (!st.listings[dir]) await loadDir(fid, dir);
      }
    }
    syncWatch(fid);
  }

  async function loadQuick(fid: string): Promise<string[]> {
    const st = folders[fid];
    if (!st) return [];
    if (!st.quick) {
      try {
        st.quick = await fsApi.files(fid);
      } catch {
        return [];
      }
    }
    return st.quick;
  }

  // ---- buffers and tabs ---------------------------------------------------

  function disposeModel(uri: string) {
    const m = models.get(uri);
    if (!m) return;
    const loc = locate(uri);
    subs.get(uri)?.dispose();
    subs.delete(uri);
    versions.delete(uri);
    diagnostics.delete(uri);
    models.delete(uri);
    if (loc)
      for (const cb of closed)
        cb({
          uri,
          path: loc.path,
          folderId: loc.folderId,
          language: languageOf(loc.path),
          model: m,
        });
    m.dispose();
  }

  function makeModel(
    fid: string,
    path: string,
    text: string,
    eol: "LF" | "CRLF",
  ) {
    const m = monaco.value!;
    const b = folders[fid].buffers[path];
    const uri = m.Uri.file(join(folders[fid].folder.path, path));
    if (models.has(uri.toString())) disposeModel(uri.toString());
    const model = m.editor.createModel(text, b.language, uri);
    model.setEOL(
      eol === "CRLF"
        ? m.editor.EndOfLineSequence.CRLF
        : m.editor.EndOfLineSequence.LF,
    );
    const key = model.uri.toString();
    models.set(key, model);
    versions.set(key, model.getAlternativeVersionId());
    subs.set(
      key,
      model.onDidChangeContent(() => {
        const buf = folders[fid]?.buffers[path];
        if (!buf) return;
        buf.dirty = model.getAlternativeVersionId() !== versions.get(key);
        if (buf.dirty) pin(fid, path);
      }),
    );
    for (const cb of opened) cb(infoOf(fid, path, model));
    return model;
  }

  function pin(fid: string, path: string) {
    const t = folders[fid]?.tabs.find((x) => x.path === path);
    if (t?.preview) {
      t.preview = false;
      persist(fid);
    }
  }

  async function ensureBuffer(fid: string, path: string): Promise<boolean> {
    const st = folders[fid];
    if (!st) return false;
    let b = st.buffers[path];
    if (b && (b.status === "ready" || b.status === "placeholder")) return true;
    if (b?.status === "loading" && inflight.has(fid + "\0" + path))
      return inflight.get(fid + "\0" + path)!;
    b = st.buffers[path] = newBuffer(path);
    const p = (async () => {
      try {
        monaco.value ??= await loadMonaco();
        const r = await fsApi.read(fid, path);
        const buf = folders[fid]?.buffers[path];
        if (!buf) return false;
        Object.assign(buf, {
          status: "ready",
          baseHash: r.hash,
          mtime: r.mtime,
          size: r.size,
          eol: r.eol,
          bom: r.bom,
          readonly: r.readonly,
        });
        makeModel(fid, path, r.content, r.eol);
        return true;
      } catch (e) {
        const buf = folders[fid]?.buffers[path];
        if (!buf) return false;
        if (e instanceof FsError && (e.status === 413 || e.status === 415)) {
          buf.status = "placeholder";
          buf.placeholder = { code: e.status, size: sizeFromBody(e) };
          return true;
        }
        if (isGone(e)) {
          markGone(fid);
          return false;
        }
        if (e instanceof FsError && e.status === 404) {
          dropTab(fid, path);
          return false;
        }
        buf.status = "error";
        buf.error = msg(e);
        return false;
      } finally {
        inflight.delete(fid + "\0" + path);
      }
    })();
    inflight.set(fid + "\0" + path, p);
    return p;
  }
  const inflight = new Map<string, Promise<boolean>>();

  function sizeFromBody(e: FsError): number | undefined {
    const b = e.body as { size?: number; error?: { size?: number } } | null;
    return b?.size ?? b?.error?.size;
  }

  async function restoreTabs(fid: string) {
    const st = folders[fid];
    if (!st) return;
    const order = [...st.tabs.map((t) => t.path)].sort(
      (a, b) => Number(b === st.active) - Number(a === st.active),
    );
    for (const p of order) {
      if (!folders[fid] || folders[fid].status !== "ready") return;
      await ensureBuffer(fid, p);
    }
    if (st.tabs.length && !st.tabs.some((t) => t.path === st.active))
      st.active = st.tabs[0].path;
    syncWatch(fid);
  }

  async function openFile(
    fid: string,
    path: string,
    opts: { line?: number; col?: number; pin?: boolean } = {},
  ) {
    const st = folders[fid];
    if (!st || st.status !== "ready") return;
    let tab = st.tabs.find((t) => t.path === path);
    if (!tab) {
      tab = { path, preview: !opts.pin };
      if (tab.preview) {
        const i = st.tabs.findIndex(
          (t) =>
            t.preview && !st.buffers[t.path]?.dirty && t.path !== st.active,
        );
        const j =
          i >= 0
            ? i
            : st.tabs.findIndex((t) => t.preview && !st.buffers[t.path]?.dirty);
        if (j >= 0) {
          const old = st.tabs[j].path;
          st.tabs.splice(j, 1, tab);
          releaseBuffer(fid, old);
        } else st.tabs.push(tab);
      } else st.tabs.push(tab);
    } else if (opts.pin) tab.preview = false;
    st.active = path;
    if (opts.line)
      reveal.value = {
        folderId: fid,
        path,
        line: opts.line,
        col: opts.col ?? 1,
        n: ++revealSeq,
      };
    void revealInTree(fid, path);
    persist(fid);
    syncWatch(fid);
    await ensureBuffer(fid, path);
  }

  function releaseBuffer(fid: string, path: string) {
    const st = folders[fid];
    if (!st) return;
    const m = modelOf(fid, path);
    if (m) disposeModel(m.uri.toString());
    delete st.buffers[path];
  }

  function dropTab(fid: string, path: string) {
    const st = folders[fid];
    if (!st) return;
    const i = st.tabs.findIndex((t) => t.path === path);
    if (i < 0) return;
    st.tabs.splice(i, 1);
    releaseBuffer(fid, path);
    if (st.active === path)
      st.active = (st.tabs[i] ?? st.tabs[i - 1])?.path ?? "";
    persist(fid);
    syncWatch(fid);
    if (!st.tabs.length && fid !== currentId.value) {
      sockets.get(fid)?.close();
      sockets.delete(fid);
    }
  }

  function closeTab(fid: string, path: string) {
    dropTab(fid, path);
  }

  function requestClose(fid: string, path: string) {
    if (folders[fid]?.buffers[path]?.dirty)
      closeRequest.value = { folderId: fid, paths: [path] };
    else closeTab(fid, path);
  }

  async function answerClose(choice: CloseChoice) {
    const req = closeRequest.value;
    closeRequest.value = null;
    if (!req || choice === "cancel") return;
    for (const p of req.paths) {
      if (choice === "save" && !(await save(req.folderId, p))) continue;
      closeTab(req.folderId, p);
    }
  }

  function setActive(fid: string, path: string) {
    const st = folders[fid];
    if (!st || !st.tabs.some((t) => t.path === path)) return;
    st.active = path;
    persist(fid);
    void ensureBuffer(fid, path);
  }

  function pinTab(fid: string, path: string) {
    pin(fid, path);
  }

  function setCursor(fid: string, path: string, line: number, col: number) {
    const t = folders[fid]?.tabs.find((x) => x.path === path);
    if (!t) return;
    t.cursor = { line, col };
    persist(fid);
  }

  // ---- save, reload, banner ----------------------------------------------

  async function save(fid: string, path: string): Promise<boolean> {
    const st = folders[fid];
    const b = st?.buffers[path];
    const model = modelOf(fid, path);
    if (!st || !b || b.status !== "ready" || !model || b.saving || b.readonly)
      return false;
    const sentVersion = model.getAlternativeVersionId();
    b.saving = true;
    b.saveError = "";
    try {
      const r = await fsApi.write(fid, {
        path,
        content: model.getValue(),
        base_hash: b.recreate ? "" : b.baseHash,
        ...(b.recreate ? { create: true } : {}),
      });
      b.baseHash = r.hash;
      b.lastSaved = r.hash;
      b.mtime = r.mtime;
      b.recreate = false;
      b.banner = null;
      versions.set(model.uri.toString(), sentVersion);
      b.dirty = model.getAlternativeVersionId() !== sentVersion;
      for (const cb of saved) cb(model.uri.toString());
      return true;
    } catch (e) {
      const c = conflictOf(e);
      if (c) {
        b.banner = {
          kind: "conflict",
          hash: c.current_hash,
          mtime: c.mtime,
          actor: (c.actor as Actor | null) ?? null,
          comparing: false,
        };
      } else if (isGone(e)) markGone(fid);
      else {
        b.saveError = msg(e);
        toast(`Could not save ${path.split("/").pop()}: ${msg(e)}`);
      }
      return false;
    } finally {
      b.saving = false;
    }
  }

  async function saveAll(fid?: string): Promise<boolean> {
    let ok = true;
    for (const [id, st] of Object.entries(folders)) {
      if (fid && id !== fid) continue;
      for (const [path, b] of Object.entries(st.buffers))
        if (b.dirty && !b.readonly) ok = (await save(id, path)) && ok;
    }
    return ok;
  }

  async function reloadBuffer(fid: string, path: string) {
    const b = folders[fid]?.buffers[path];
    const model = modelOf(fid, path);
    if (!b || !model) return;
    try {
      const r = await fsApi.read(fid, path);
      model.pushEditOperations(
        [],
        [{ range: model.getFullModelRange(), text: r.content }],
        () => null,
      );
      model.pushStackElement();
      versions.set(model.uri.toString(), model.getAlternativeVersionId());
      Object.assign(b, {
        baseHash: r.hash,
        mtime: r.mtime,
        size: r.size,
        dirty: false,
        banner: null,
        recreate: false,
        readonly: r.readonly,
      });
    } catch (e) {
      if (e instanceof FsError && e.status === 404)
        b.banner = {
          kind: "deleted",
          hash: "",
          mtime: "",
          actor: null,
          comparing: false,
        };
      else toast(`Could not reload ${path.split("/").pop()}: ${msg(e)}`);
    }
  }

  function keepMine(fid: string, path: string) {
    const b = folders[fid]?.buffers[path];
    if (!b?.banner) return;
    if (b.banner.kind === "deleted") b.recreate = true;
    else b.baseHash = b.banner.hash;
    b.banner = null;
  }

  function setComparing(fid: string, path: string, on: boolean) {
    const b = folders[fid]?.buffers[path];
    if (b?.banner) b.banner.comparing = on;
  }

  async function diskCopy(fid: string, path: string): Promise<string> {
    return (await fsApi.read(fid, path)).content;
  }

  // ---- explorer operations -----------------------------------------------

  async function createFile(fid: string, path: string) {
    await fsApi.write(fid, { path, content: "", base_hash: "", create: true });
    await loadDir(fid, dirOf(path));
    await openFile(fid, path, { pin: true });
  }

  async function createFolder(fid: string, path: string) {
    await fsApi.mkdir(fid, path);
    await loadDir(fid, dirOf(path));
    const st = folders[fid];
    if (st && !st.expanded.includes(dirOf(path)) && dirOf(path))
      st.expanded.push(dirOf(path));
  }

  async function renamePath(fid: string, from: string, to: string) {
    await fsApi.rename(fid, from, to);
    const st = folders[fid];
    if (!st) return;
    for (const t of [...st.tabs]) {
      if (t.path !== from && !t.path.startsWith(`${from}/`)) continue;
      const next = to + t.path.slice(from.length);
      const b = st.buffers[t.path];
      const m = modelOf(fid, t.path);
      const text = m?.getValue() ?? "";
      const dirty = !!b?.dirty;
      if (m) disposeModel(m.uri.toString());
      delete st.buffers[t.path];
      t.path = next;
      const nb = (st.buffers[next] = newBuffer(next));
      if (b)
        Object.assign(nb, { ...b, path: next, language: languageOf(next) });
      if (b && m && monaco.value) {
        makeModel(fid, next, text, b.eol);
        if (dirty) {
          nb.dirty = true;
          versions.set(modelOf(fid, next)!.uri.toString(), -1);
        }
      }
    }
    if (st.active === from) st.active = to;
    else if (st.active.startsWith(`${from}/`))
      st.active = to + st.active.slice(from.length);
    for (const k of Object.keys(st.listings))
      if (k === from || k.startsWith(`${from}/`)) delete st.listings[k];
    st.expanded = st.expanded.filter(
      (d) => d !== from && !d.startsWith(`${from}/`),
    );
    st.quick = null;
    await Promise.all([
      loadDir(fid, dirOf(from)),
      dirOf(to) === dirOf(from) ? 0 : loadDir(fid, dirOf(to)),
    ]);
    persist(fid);
    syncWatch(fid);
  }

  // ---- diagnostics helpers (the language client fills `diagnostics`) -----

  function errorCounts(fid: string): Map<string, number> {
    const per = new Map<string, number>();
    for (const [uri, list] of diagnostics) {
      const loc = locate(uri);
      if (!loc || loc.folderId !== fid) continue;
      const n = list.filter((d) => d.severity === 1).length;
      if (n) per.set(loc.path, n);
    }
    return rollupCounts(per);
  }

  function setServerState(id: string, s: Omit<ServerState, "server">) {
    serverStates[id] = { server: id, ...s };
  }

  // ---- guards ------------------------------------------------------------

  function dirtyPaths(): { folderId: string; path: string }[] {
    const out: { folderId: string; path: string }[] = [];
    for (const [fid, st] of Object.entries(folders))
      for (const [path, b] of Object.entries(st.buffers))
        if (b.dirty) out.push({ folderId: fid, path });
    return out;
  }

  function confirmLeave(): Promise<boolean> {
    if (!dirtyPaths().length) return Promise.resolve(true);
    return new Promise((resolve) => {
      leavePrompt.value = { resolve };
    });
  }

  async function answerLeave(choice: CloseChoice) {
    const p = leavePrompt.value;
    leavePrompt.value = null;
    if (!p) return;
    if (choice === "cancel") return p.resolve(false);
    if (choice === "save") return p.resolve(await saveAll());
    p.resolve(true);
  }

  return {
    folders,
    projects,
    currentId,
    workspaceProject,
    monaco,
    models,
    diagnostics,
    serverStates,
    reveal,
    closeRequest,
    leavePrompt,
    quickOpen,
    current,
    folder,
    onModelOpened,
    onModelClosed,
    onModelSaved,
    openFileAt,
    ensureModel,
    uriOf,
    modelOf,
    locate,
    attach,
    detach,
    hydrate,
    openProject,
    useFolder,
    openWorktree,
    openFile,
    closeTab,
    requestClose,
    answerClose,
    setActive,
    pinTab,
    setCursor,
    save,
    saveAll,
    reloadBuffer,
    keepMine,
    setComparing,
    diskCopy,
    loadDir,
    revealInTree,
    toggleDir,
    loadQuick,
    createFile,
    createFolder,
    renamePath,
    errorCounts,
    setServerState,
    dirtyPaths,
    confirmLeave,
    answerLeave,
    persistNow,
    onFrame,
  };
});

export type EditorStore = ReturnType<typeof useEditor>;
