/* eslint-disable @typescript-eslint/no-explicit-any -- Monaco and the editor store are owned by builder C; typed structurally until they land */
import { reactive, ref } from "vue";
import {
  AbstractMessageReader,
  AbstractMessageWriter,
  createMessageConnection,
  ErrorCodes,
  ResponseError,
  type DataCallback,
  type Disposable,
  type Message,
  type MessageConnection,
  type MessageReader,
  type MessageWriter,
} from "vscode-jsonrpc/browser";
import type {
  CodeAction,
  CompletionItem,
  Diagnostic,
  InitializeResult,
  Location,
  LocationLink,
  Range,
  WorkspaceEdit,
} from "vscode-languageserver-protocol/browser";
import { api } from "../../types/api";
import { useToast } from "../../composables/useToast";

export type ServerId = "gopls" | "tsls";
export type ConnState =
  | "idle"
  | "connecting"
  | "running"
  | "taken_over"
  | "not_found"
  | "untrusted"
  | "too_many"
  | "crashed"
  | "disconnected";
export type LspProgress = {
  title: string;
  message?: string;
  percentage?: number;
};

export type ServerInfo = {
  id: string;
  languages: string[];
  state: "found" | "not_found" | "outdated" | "running" | "crashed";
  version?: string;
  path?: string;
  install?: string;
  error?: string;
};

export interface LspStore {
  monaco: any | null;
  models: Map<string, any>;
  folder: { id: string; path: string } | null;
  diagnostics: Map<string, Diagnostic[]>;
  onModelOpened(cb: (info: { uri: string; model: any }) => void): (() => void) | void;
  onModelClosed(cb: (info: { uri: string }) => void): (() => void) | void;
  onModelSaved?(cb: (uri: string) => void): (() => void) | void;
  openFileAt?(
    uri: string,
    pos?: { line: number; column: number },
  ): void | Promise<void>;
  openExternal?(
    uri: string,
    pos?: { line: number; column: number },
  ): void | Promise<void>;
  ensureModel?(uri: string): Promise<any | null>;
}

const EXT_LANG: Record<string, [ServerId, string]> = {
  go: ["gopls", "go"],
  ts: ["tsls", "typescript"],
  tsx: ["tsls", "typescriptreact"],
  js: ["tsls", "javascript"],
  jsx: ["tsls", "javascriptreact"],
};
const NAME_LANG: Record<string, ServerId> = {
  go: "gopls",
  "go.mod": "gopls",
  typescript: "tsls",
  typescriptreact: "tsls",
  javascript: "tsls",
  javascriptreact: "tsls",
  ts: "tsls",
  tsx: "tsls",
  js: "tsls",
  jsx: "tsls",
};
const MONACO_LANGS: Record<ServerId, string[]> = {
  gopls: ["go"],
  tsls: ["typescript", "javascript", "typescriptreact", "javascriptreact"],
};

export function serverForLanguage(lang: string | null | undefined) {
  return lang ? (NAME_LANG[lang] ?? null) : null;
}
export function resolveDocument(
  uri: string,
  monacoLang?: string,
): { server: ServerId; languageId: string } | null {
  const base = uri.slice(uri.lastIndexOf("/") + 1);
  if (base === "go.mod") return { server: "gopls", languageId: "go.mod" };
  const ext = base.includes(".") ? base.slice(base.lastIndexOf(".") + 1) : "";
  const byExt = EXT_LANG[ext];
  if (byExt) return { server: byExt[0], languageId: byExt[1] };
  const server = serverForLanguage(monacoLang);
  return server && monacoLang ? { server, languageId: monacoLang } : null;
}

export const lspState = reactive<
  Record<string, { conn: ConnState; progress: LspProgress | null }>
>({});
function setConn(server: string, conn: ConnState) {
  const cur = lspState[server];
  if (cur) cur.conn = conn;
  else lspState[server] = { conn, progress: null };
  if (conn !== "running") lspState[server].progress = null;
}

export const servers = ref<ServerInfo[]>([]);
export const serversError = ref("");
async function applyServers(p: Promise<ServerInfo[] | null>) {
  try {
    servers.value = (await p) ?? [];
    serversError.value = "";
  } catch (e) {
    serversError.value = e instanceof Error ? e.message : String(e);
  }
}
export const loadServers = () =>
  applyServers(api.get<ServerInfo[]>("/api/v1/editor/servers"));
export const checkServers = () =>
  applyServers(api.post<ServerInfo[]>("/api/v1/editor/servers/check"));

let active: { reconnect(server: string): void } | null = null;
export function restartLanguageServer(server: string) {
  active?.reconnect(server);
}

const CLOSE_STATE: Record<number, ConnState> = {
  4001: "taken_over",
  4002: "not_found",
  4003: "untrusted",
  4004: "too_many",
  4005: "crashed",
};

class WsReader extends AbstractMessageReader implements MessageReader {
  private cb: DataCallback | null = null;
  constructor(ws: WebSocket) {
    super();
    ws.addEventListener("message", (e) => {
      if (typeof e.data !== "string") return;
      try {
        this.cb?.(JSON.parse(e.data) as Message);
      } catch (err) {
        this.fireError(err);
      }
    });
    ws.addEventListener("close", () => this.fireClose());
    ws.addEventListener("error", () => this.fireError(new Error("socket")));
  }
  listen(cb: DataCallback): Disposable {
    this.cb = cb;
    return {
      dispose: () => {
        this.cb = null;
      },
    };
  }
}
class WsWriter extends AbstractMessageWriter implements MessageWriter {
  constructor(private ws: WebSocket) {
    super();
  }
  async write(msg: Message) {
    if (this.ws.readyState !== 1) return;
    this.ws.send(JSON.stringify(msg));
  }
  end() {}
}

export const lspPos = (p: { lineNumber: number; column: number }) => ({
  line: p.lineNumber - 1,
  character: p.column - 1,
});
export const monacoRange = (r: Range) => ({
  startLineNumber: r.start.line + 1,
  startColumn: r.start.character + 1,
  endLineNumber: r.end.line + 1,
  endColumn: r.end.character + 1,
});
const lspRange = (r: any): Range => ({
  start: lspPos({ lineNumber: r.startLineNumber, column: r.startColumn }),
  end: lspPos({ lineNumber: r.endLineNumber, column: r.endColumn }),
});

const KIND_NAMES = [
  "Text",
  "Method",
  "Function",
  "Constructor",
  "Field",
  "Variable",
  "Class",
  "Interface",
  "Module",
  "Property",
  "Unit",
  "Value",
  "Enum",
  "Keyword",
  "Snippet",
  "Color",
  "File",
  "Reference",
  "Folder",
  "EnumMember",
  "Constant",
  "Struct",
  "Event",
  "Operator",
  "TypeParameter",
];
const docText = (d: any) =>
  d == null ? undefined : typeof d === "string" ? d : { value: d.value };

export function toMonacoCompletion(
  monaco: any,
  item: CompletionItem,
  defaultRange: any,
) {
  const kinds = monaco.languages.CompletionItemKind;
  const te: any = item.textEdit;
  let range = defaultRange;
  if (te) {
    range = te.range
      ? monacoRange(te.range)
      : { insert: monacoRange(te.insert), replace: monacoRange(te.replace) };
  }
  const snippet = item.insertTextFormat === 2;
  return {
    label: item.label,
    kind: kinds[KIND_NAMES[(item.kind ?? 1) - 1]] ?? kinds.Text,
    detail: item.detail,
    documentation: docText(item.documentation),
    sortText: item.sortText,
    filterText: item.filterText,
    preselect: item.preselect,
    commitCharacters: item.commitCharacters,
    insertText: te?.newText ?? item.insertText ?? item.label,
    insertTextRules: snippet
      ? monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet
      : undefined,
    range,
    additionalTextEdits: item.additionalTextEdits?.map((e) => ({
      range: monacoRange(e.range),
      text: e.newText,
    })),
    __lsp: item,
  };
}

function hoverContents(c: any): { value: string }[] {
  if (c == null) return [];
  if (Array.isArray(c)) return c.flatMap(hoverContents);
  if (typeof c === "string") return c ? [{ value: c }] : [];
  if (c.language) return [{ value: "```" + c.language + "\n" + c.value + "\n```" }];
  return c.value ? [{ value: c.value }] : [];
}

function normalizeLocations(res: any): { uri: string; range: Range }[] {
  if (!res) return [];
  return (Array.isArray(res) ? res : [res]).map(
    (l: Location | LocationLink) =>
      "targetUri" in l
        ? { uri: l.targetUri, range: l.targetSelectionRange ?? l.targetRange }
        : { uri: l.uri, range: l.range },
  );
}

export const fileUriToPath = (uri: string) =>
  decodeURIComponent(uri.replace(/^file:\/\//, ""));

const CAPABILITIES = {
  textDocument: {
    synchronization: { dynamicRegistration: false, didSave: true },
    hover: { contentFormat: ["markdown", "plaintext"] },
    definition: { linkSupport: true },
    references: {},
    publishDiagnostics: { relatedInformation: false },
    completion: {
      contextSupport: true,
      completionItem: {
        snippetSupport: true,
        insertReplaceSupport: true,
        documentationFormat: ["markdown", "plaintext"],
        resolveSupport: {
          properties: ["documentation", "detail", "additionalTextEdits"],
        },
      },
    },
    codeAction: {
      isPreferredSupport: true,
      codeActionLiteralSupport: {
        codeActionKind: { valueSet: ["quickfix"] },
      },
    },
  },
  workspace: {
    configuration: true,
    applyEdit: true,
    workspaceEdit: { documentChanges: true },
  },
  window: { workDoneProgress: true, showMessage: {} },
};

const APPLY_EDIT_CMD = "legos.lsp.applyEdit";
const sev = (m: any, s?: number) =>
  ({ 1: m.MarkerSeverity.Error, 2: m.MarkerSeverity.Warning, 3: m.MarkerSeverity.Info, 4: m.MarkerSeverity.Hint })[
    s ?? 1
  ] ?? m.MarkerSeverity.Error;

class Session {
  conn: MessageConnection | null = null;
  ws: WebSocket | null = null;
  caps: any = null;
  syncKind = 1;
  running = false;
  docs = new Map<string, { version: number; sub: { dispose(): void } }>();
  published = new Set<string>();
  tokens = new Map<string | number, LspProgress>();
  disposed = false;

  constructor(
    readonly id: ServerId,
    private c: Controller,
  ) {}

  start() {
    const folder = this.c.store.folder;
    if (!folder) return;
    setConn(this.id, "connecting");
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(
      `${proto}//${location.host}/api/v1/editor/folders/${encodeURIComponent(folder.id)}/lsp/${this.id}`,
    );
    this.ws = ws;
    let opened = false;
    ws.addEventListener("open", () => {
      opened = true;
      void this.initialize(ws, folder.path);
    });
    ws.addEventListener("close", (e) => {
      if (this.ws !== ws) return;
      const next = CLOSE_STATE[e.code] ?? "disconnected";
      this.teardown();
      if (!this.disposed) setConn(this.id, opened || e.code ? next : "disconnected");
    });
  }

  private async initialize(ws: WebSocket, path: string) {
    const conn = createMessageConnection(new WsReader(ws), new WsWriter(ws));
    this.conn = conn;
    this.registerHandlers(conn);
    conn.listen();
    const rootUri = "file://" + path;
    try {
      const res = (await conn.sendRequest("initialize", {
        processId: null,
        clientInfo: { name: "legos-editor" },
        rootUri,
        workspaceFolders: [{ uri: rootUri, name: path.split("/").pop() ?? "" }],
        capabilities: CAPABILITIES,
      })) as InitializeResult;
      if (this.ws !== ws) return;
      this.caps = res.capabilities;
      const sync: any = this.caps.textDocumentSync;
      this.syncKind = typeof sync === "number" ? sync : (sync?.change ?? 1);
      await conn.sendNotification("initialized", {});
      this.running = true;
      setConn(this.id, "running");
      for (const [uri, model] of this.c.store.models) this.track(uri, model);
    } catch {
      if (this.ws === ws && !this.disposed) {
        this.teardown();
        setConn(this.id, "disconnected");
      }
    }
  }

  private registerHandlers(conn: MessageConnection) {
    const { toast } = useToast();
    conn.onNotification(
      "textDocument/publishDiagnostics",
      (p: { uri: string; diagnostics: Diagnostic[] }) =>
        this.c.publish(this, p.uri, p.diagnostics),
    );
    conn.onNotification("window/showMessage", (p: { message: string }) =>
      toast(p.message),
    );
    conn.onNotification("$/progress", (p: { token: string | number; value: any }) => {
      const v = p.value ?? {};
      if (v.kind === "end") this.tokens.delete(p.token);
      else
        this.tokens.set(p.token, {
          title: v.title ?? this.tokens.get(p.token)?.title ?? "",
          message: v.message,
          percentage: v.percentage,
        });
      const last = [...this.tokens.values()].pop() ?? null;
      if (lspState[this.id]) lspState[this.id].progress = last;
    });
    conn.onRequest("workspace/configuration", (p: { items: unknown[] }) =>
      p.items.map(() => null),
    );
    conn.onRequest("client/registerCapability", () => null);
    conn.onRequest("client/unregisterCapability", () => null);
    conn.onRequest("window/workDoneProgress/create", () => null);
    conn.onRequest("workspace/applyEdit", async (p: { edit: WorkspaceEdit }) => ({
      applied: await this.c.applyWorkspaceEdit(p.edit),
    }));
    conn.onRequest((method) => {
      void method;
      return new ResponseError(ErrorCodes.MethodNotFound, "Method not found");
    });
  }

  track(uri: string, model: any) {
    if (!this.running || this.docs.has(uri)) return;
    const doc = resolveDocument(uri, model.getLanguageId?.());
    if (!doc || doc.server !== this.id) return;
    const entry = { version: model.getVersionId(), sub: { dispose() {} } };
    this.docs.set(uri, entry);
    void this.conn?.sendNotification("textDocument/didOpen", {
      textDocument: {
        uri,
        languageId: doc.languageId,
        version: entry.version,
        text: model.getValue(),
      },
    });
    entry.sub = model.onDidChangeContent((e: any) => {
      entry.version = model.getVersionId();
      const contentChanges =
        this.syncKind === 2
          ? e.changes.map((c: any) => ({
              range: lspRange(c.range),
              rangeLength: c.rangeLength,
              text: c.text,
            }))
          : [{ text: model.getValue() }];
      void this.conn?.sendNotification("textDocument/didChange", {
        textDocument: { uri, version: entry.version },
        contentChanges,
      });
    });
  }

  untrack(uri: string) {
    const d = this.docs.get(uri);
    if (!d) return;
    d.sub.dispose();
    this.docs.delete(uri);
    void this.conn?.sendNotification("textDocument/didClose", {
      textDocument: { uri },
    });
  }

  saved(uri: string) {
    if (!this.docs.has(uri)) return;
    const text = this.c.store.models.get(uri)?.getValue();
    void this.conn?.sendNotification("textDocument/didSave", {
      textDocument: { uri },
      text,
    });
  }

  request<T>(method: string, params: unknown): Promise<T | null> {
    if (!this.running || !this.conn) return Promise.resolve(null);
    return (this.conn.sendRequest(method, params) as Promise<T>).catch(() => null);
  }

  teardown() {
    this.running = false;
    for (const d of this.docs.values()) d.sub.dispose();
    this.docs.clear();
    this.tokens.clear();
    for (const uri of [...this.published]) this.c.publish(this, uri, []);
    this.published.clear();
    this.conn?.dispose();
    this.conn = null;
    this.ws = null;
  }

  dispose() {
    this.disposed = true;
    const ws = this.ws;
    this.teardown();
    ws?.close();
    setConn(this.id, "idle");
  }
}

class Controller {
  sessions = new Map<ServerId, Session>();
  disposers: (() => void)[] = [];

  constructor(readonly store: LspStore) {}

  get monaco() {
    return this.store.monaco;
  }

  start() {
    const store = this.store;
    const subs = [
      store.onModelOpened(({ uri, model }) =>
        this.opened(uri, model ?? store.models.get(uri)),
      ),
      store.onModelClosed(({ uri }) => {
        for (const s of this.sessions.values()) s.untrack(uri);
      }),
      store.onModelSaved?.((uri) => {
        for (const s of this.sessions.values()) s.saved(uri);
      }),
    ];
    for (const u of subs) if (u) this.disposers.push(u);
    for (const [uri, model] of store.models) this.opened(uri, model);
    this.registerMonaco();
  }

  opened(uri: string, model: any) {
    const doc = resolveDocument(uri, model?.getLanguageId?.());
    if (!doc) return;
    let s = this.sessions.get(doc.server);
    if (!s) {
      s = new Session(doc.server, this);
      this.sessions.set(doc.server, s);
      s.start();
    } else s.track(uri, model);
    this.applyMarkers(uri, model);
  }

  reconnect(server: string) {
    const old = this.sessions.get(server as ServerId);
    if (!old) return;
    old.dispose();
    const s = new Session(old.id, this);
    this.sessions.set(old.id, s);
    s.start();
  }

  publish(s: Session, uri: string, diagnostics: Diagnostic[]) {
    if (diagnostics.length) {
      s.published.add(uri);
      this.store.diagnostics.set(uri, diagnostics);
    } else {
      s.published.delete(uri);
      this.store.diagnostics.delete(uri);
    }
    const model = this.store.models.get(uri);
    if (model) this.applyMarkers(uri, model);
  }

  applyMarkers(uri: string, model: any) {
    const m = this.monaco;
    if (!m) return;
    const diags = this.store.diagnostics.get(uri) ?? [];
    m.editor.setModelMarkers(
      model,
      "lsp",
      diags.map((d) => ({
        ...monacoRange(d.range),
        severity: sev(m, d.severity),
        message: d.message,
        source: d.source,
        code: d.code == null ? undefined : String(d.code),
      })),
    );
  }

  sessionFor(model: any) {
    const doc = resolveDocument(model.uri.toString(), model.getLanguageId?.());
    const s = doc && this.sessions.get(doc.server);
    return s?.running ? s : null;
  }

  insideFolder(uri: string) {
    const root = this.store.folder?.path;
    return !!root && fileUriToPath(uri).startsWith(root.replace(/\/$/, "") + "/");
  }

  async openTarget(uri: string, range?: Range) {
    const pos = range
      ? { line: range.start.line + 1, column: range.start.character + 1 }
      : undefined;
    if (this.insideFolder(uri) && this.store.openFileAt) {
      await this.store.openFileAt(uri, pos);
      return true;
    }
    if (!this.insideFolder(uri) && this.store.openExternal) {
      await this.store.openExternal(uri, pos);
      return true;
    }
    if (!this.insideFolder(uri))
      useToast().toast(
        `Definition is outside this folder: ${fileUriToPath(uri)}`,
      );
    return false;
  }

  async applyWorkspaceEdit(edit: WorkspaceEdit) {
    const perUri = new Map<string, { range: Range; newText: string }[]>();
    for (const [uri, edits] of Object.entries(edit.changes ?? {}))
      perUri.set(uri, edits);
    for (const dc of edit.documentChanges ?? []) {
      if (!("textDocument" in dc)) return false;
      const list = perUri.get(dc.textDocument.uri) ?? [];
      list.push(...(dc.edits as { range: Range; newText: string }[]));
      perUri.set(dc.textDocument.uri, list);
    }
    const targets: [any, { range: Range; newText: string }[]][] = [];
    for (const [uri, edits] of perUri) {
      const model =
        this.store.models.get(uri) ?? (await this.store.ensureModel?.(uri));
      if (!model) return false;
      targets.push([model, edits]);
    }
    for (const [model, edits] of targets)
      model.pushEditOperations(
        [],
        edits.map((e) => ({ range: monacoRange(e.range), text: e.newText })),
        () => null,
      );
    return true;
  }

  registerMonaco() {
    const m = this.monaco;
    if (!m) return;
    const add = (d: { dispose(): void } | undefined) =>
      d && this.disposers.push(() => d.dispose());
    const uriOf = (model: any) => model.uri.toString();
    const toLoc = (l: { uri: string; range: Range }) => ({
      uri: m.Uri.parse(l.uri),
      range: monacoRange(l.range),
    });
    const docPos = (model: any, position: any) => ({
      textDocument: { uri: uriOf(model) },
      position: lspPos(position),
    });

    for (const id of Object.keys(MONACO_LANGS) as ServerId[]) {
      for (const lang of MONACO_LANGS[id]) {
        add(
          m.languages.registerHoverProvider(lang, {
            provideHover: async (model: any, position: any) => {
              const res = await this.sessionFor(model)?.request<any>(
                "textDocument/hover",
                docPos(model, position),
              );
              const contents = hoverContents(res?.contents);
              if (!res || !contents.length) return null;
              return {
                contents,
                range: res.range ? monacoRange(res.range) : undefined,
              };
            },
          }),
        );
        add(
          m.languages.registerDefinitionProvider(lang, {
            provideDefinition: async (model: any, position: any) => {
              const res = await this.sessionFor(model)?.request(
                "textDocument/definition",
                docPos(model, position),
              );
              return normalizeLocations(res).map(toLoc);
            },
          }),
        );
        add(
          m.languages.registerReferenceProvider(lang, {
            provideReferences: async (model: any, position: any) => {
              const res = await this.sessionFor(model)?.request(
                "textDocument/references",
                { ...docPos(model, position), context: { includeDeclaration: true } },
              );
              return normalizeLocations(res).map(toLoc);
            },
          }),
        );
        add(
          m.languages.registerCompletionItemProvider(lang, {
            triggerCharacters: this.sessions.get(id)?.caps?.completionProvider
              ?.triggerCharacters ?? [".", '"', "'", "/", "@", "<"],
            provideCompletionItems: async (model: any, position: any, context: any) => {
              const word = model.getWordUntilPosition(position);
              const range = {
                startLineNumber: position.lineNumber,
                endLineNumber: position.lineNumber,
                startColumn: word.startColumn,
                endColumn: position.column,
              };
              const res = await this.sessionFor(model)?.request<any>(
                "textDocument/completion",
                {
                  ...docPos(model, position),
                  context: {
                    triggerKind: (context?.triggerKind ?? 0) + 1,
                    triggerCharacter: context?.triggerCharacter,
                  },
                },
              );
              if (!res) return { suggestions: [] };
              const items: CompletionItem[] = Array.isArray(res) ? res : res.items;
              return {
                suggestions: items.map((i) => toMonacoCompletion(m, i, range)),
                incomplete: !Array.isArray(res) && !!res.isIncomplete,
              };
            },
            resolveCompletionItem: async (item: any) => {
              const s = this.sessions.get(id);
              if (!item.__lsp || !s?.caps?.completionProvider?.resolveProvider)
                return item;
              const r = await s.request<CompletionItem>(
                "completionItem/resolve",
                item.__lsp,
              );
              if (r) {
                item.detail = r.detail ?? item.detail;
                item.documentation = docText(r.documentation) ?? item.documentation;
                if (r.additionalTextEdits)
                  item.additionalTextEdits = r.additionalTextEdits.map((e) => ({
                    range: monacoRange(e.range),
                    text: e.newText,
                  }));
              }
              return item;
            },
          }),
        );
        add(
          m.languages.registerCodeActionProvider(lang, {
            provideCodeActions: async (model: any, range: any, context: any) => {
              const markers: any[] = context?.markers ?? [];
              if (!markers.length) return { actions: [], dispose() {} };
              const known = this.store.diagnostics.get(uriOf(model)) ?? [];
              const diagnostics = markers.map(
                (mk) =>
                  known.find(
                    (d) =>
                      d.message === mk.message &&
                      d.range.start.line === mk.startLineNumber - 1 &&
                      d.range.start.character === mk.startColumn - 1,
                  ) ?? {
                    range: lspRange(mk),
                    message: mk.message,
                    severity: 1,
                  },
              );
              const res = await this.sessionFor(model)?.request<CodeAction[]>(
                "textDocument/codeAction",
                {
                  textDocument: { uri: uriOf(model) },
                  range: lspRange(range),
                  context: { diagnostics, only: ["quickfix"] },
                },
              );
              const actions = (res ?? [])
                .filter((a) => a.edit)
                .map((a) => ({
                  title: a.title,
                  kind: a.kind ?? "quickfix",
                  isPreferred: a.isPreferred,
                  diagnostics: markers,
                  command: {
                    id: APPLY_EDIT_CMD,
                    title: a.title,
                    arguments: [a.edit],
                  },
                }));
              return { actions, dispose() {} };
            },
          }),
        );
      }
    }
    add(
      m.editor.registerCommand?.(APPLY_EDIT_CMD, (_a: unknown, edit: WorkspaceEdit) =>
        this.applyWorkspaceEdit(edit),
      ),
    );
    add(
      m.editor.registerEditorOpener?.({
        openCodeEditor: async (_src: unknown, resource: any, selection: any) => {
          const range = selection
            ? lspRange({
                startLineNumber: selection.startLineNumber ?? selection.lineNumber,
                startColumn: selection.startColumn ?? selection.column,
                endLineNumber: selection.endLineNumber ?? selection.lineNumber,
                endColumn: selection.endColumn ?? selection.column,
              })
            : undefined;
          return this.openTarget(resource.toString(), range);
        },
      }),
    );
  }

  dispose() {
    for (const s of this.sessions.values()) s.dispose();
    this.sessions.clear();
    for (const d of this.disposers.splice(0)) d();
    if (active === this) active = null;
  }
}

export function connectLanguageServers(store: LspStore): () => void {
  const c = new Controller(store);
  active = c;
  c.start();
  return () => c.dispose();
}
