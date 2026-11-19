/* eslint-disable @typescript-eslint/no-explicit-any -- fake Monaco/WebSocket doubles */
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  connectLanguageServers,
  lspState,
  resolveDocument,
  toMonacoCompletion,
  type LspStore,
} from "../../src/features/editor/lsp";

class FakeWS extends EventTarget {
  static all: FakeWS[] = [];
  readyState = 0;
  sent: any[] = [];
  constructor(public url: string) {
    super();
    FakeWS.all.push(this);
  }
  send(data: string) {
    const msg = JSON.parse(data);
    this.sent.push(msg);
    if (msg.method === "initialize")
      this.push({
        id: msg.id,
        result: {
          capabilities: {
            textDocumentSync: 2,
            completionProvider: { resolveProvider: false },
          },
        },
      });
  }
  push(obj: unknown) {
    this.dispatchEvent(
      new MessageEvent("message", { data: JSON.stringify(obj) }),
    );
  }
  open() {
    this.readyState = 1;
    this.dispatchEvent(new Event("open"));
  }
  close(code = 1000) {
    this.readyState = 3;
    const e = new Event("close") as Event & { code: number };
    e.code = code;
    this.dispatchEvent(e);
  }
  methods = () => this.sent.map((m) => m.method).filter(Boolean);
}

function fakeModel(uri: string, lang: string, text = "package a\n") {
  let version = 1;
  const listeners: ((e: any) => void)[] = [];
  return {
    uri: { toString: () => uri },
    getLanguageId: () => lang,
    getVersionId: () => version,
    getValue: () => text,
    getWordUntilPosition: () => ({ startColumn: 1 }),
    onDidChangeContent: (cb: (e: any) => void) => {
      listeners.push(cb);
      return { dispose: () => listeners.splice(listeners.indexOf(cb), 1) };
    },
    edit(changes: any[]) {
      version++;
      listeners.forEach((l) => l({ changes }));
    },
    pushEditOperations: vi.fn(),
    listeners,
  };
}

function fakeMonaco() {
  const providers: Record<string, any> = {};
  const reg = (kind: string) => (_lang: string, p: any) => {
    providers[kind] = p;
    return { dispose() {} };
  };
  return {
    providers,
    markers: [] as any[],
    languages: {
      registerHoverProvider: reg("hover"),
      registerDefinitionProvider: reg("definition"),
      registerReferenceProvider: reg("references"),
      registerCompletionItemProvider: reg("completion"),
      registerCodeActionProvider: reg("codeAction"),
      CompletionItemKind: { Text: 18, Function: 1, Method: 0 },
      CompletionItemInsertTextRule: { InsertAsSnippet: 4 },
    },
    editor: {
      setModelMarkers: vi.fn(),
      registerCommand: () => ({ dispose() {} }),
      registerEditorOpener: () => ({ dispose() {} }),
    },
    MarkerSeverity: { Error: 8, Warning: 4, Info: 2, Hint: 1 },
    Uri: { parse: (s: string) => ({ toString: () => s }) },
  };
}

function setup(models: Record<string, any>) {
  const monaco = fakeMonaco();
  const opened: ((uri: string, m: any) => void)[] = [];
  const store: LspStore = {
    monaco,
    models: new Map(Object.entries(models)),
    folder: { id: "f1", path: "/work" },
    diagnostics: new Map(),
    onModelOpened: (cb) => {
      opened.push(cb);
    },
    onModelClosed: () => {},
    openFileAt: vi.fn(),
  };
  return { monaco, store, opened };
}

beforeEach(() => {
  FakeWS.all = [];
  vi.stubGlobal("WebSocket", FakeWS);
  for (const k of Object.keys(lspState)) delete lspState[k];
});
afterEach(() => vi.unstubAllGlobals());

const connectGo = async () => {
  const model = fakeModel("file:///work/a.go", "go");
  const ctx = setup({ "file:///work/a.go": model });
  const dispose = connectLanguageServers(ctx.store);
  const ws = FakeWS.all[0];
  ws.open();
  await vi.waitFor(() => expect(lspState.gopls.conn).toBe("running"));
  return { ...ctx, model, ws, dispose };
};

it("maps files to servers", () => {
  expect(resolveDocument("file:///w/go.mod")).toEqual({
    server: "gopls",
    languageId: "go.mod",
  });
  expect(resolveDocument("file:///w/a.tsx", "typescript")?.languageId).toBe(
    "typescriptreact",
  );
  expect(resolveDocument("file:///w/a.md", "markdown")).toBeNull();
});

it("opens the socket, initializes, and sends didOpen then incremental didChange", async () => {
  const { ws, model } = await connectGo();
  expect(ws.url).toBe("ws://localhost:3000/api/v1/editor/folders/f1/lsp/gopls");
  const init = ws.sent.find((m) => m.method === "initialize");
  expect(init.params.rootUri).toBe("file:///work");
  expect(Object.keys(init.params.capabilities.textDocument).sort()).toEqual([
    "codeAction",
    "completion",
    "definition",
    "hover",
    "publishDiagnostics",
    "references",
    "synchronization",
  ]);
  expect(ws.methods()).toEqual([
    "initialize",
    "initialized",
    "textDocument/didOpen",
  ]);
  expect(ws.sent[2].params.textDocument.languageId).toBe("go");
  model.edit([
    {
      range: { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 2 },
      rangeLength: 1,
      text: "x",
    },
  ]);
  const ch = ws.sent.at(-1);
  expect(ch.method).toBe("textDocument/didChange");
  expect(ch.params.contentChanges[0]).toEqual({
    range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } },
    rangeLength: 1,
    text: "x",
  });
});

it("answers hover through the Monaco provider", async () => {
  const { ws, model, monaco } = await connectGo();
  const p = monaco.providers.hover.provideHover(model, {
    lineNumber: 3,
    column: 5,
  });
  const req = await vi.waitFor(() => {
    const r = ws.sent.find((m) => m.method === "textDocument/hover");
    expect(r).toBeTruthy();
    return r;
  });
  expect(req.params.position).toEqual({ line: 2, character: 4 });
  ws.push({
    id: req.id,
    result: { contents: { kind: "markdown", value: "**func**" } },
  });
  expect((await p).contents).toEqual([{ value: "**func**" }]);
});

it("publishDiagnostics lands in the store and markers", async () => {
  const { ws, store, monaco } = await connectGo();
  const diag = {
    range: { start: { line: 1, character: 2 }, end: { line: 1, character: 5 } },
    message: "undefined: x",
    severity: 1,
  };
  ws.push({
    method: "textDocument/publishDiagnostics",
    params: { uri: "file:///work/a.go", diagnostics: [diag] },
  });
  await vi.waitFor(() =>
    expect(store.diagnostics.get("file:///work/a.go")).toEqual([diag]),
  );
  const last = monaco.editor.setModelMarkers.mock.calls.at(-1)!;
  expect(last[2][0]).toMatchObject({
    startLineNumber: 2,
    startColumn: 3,
    severity: 8,
    message: "undefined: x",
  });
  ws.push({
    method: "textDocument/publishDiagnostics",
    params: { uri: "file:///work/a.go", diagnostics: [] },
  });
  await vi.waitFor(() => expect(store.diagnostics.size).toBe(0));
});

it("maps completion items", () => {
  const m = fakeMonaco();
  const item = toMonacoCompletion(
    m,
    {
      label: "Println",
      kind: 3,
      insertText: "Println($0)",
      insertTextFormat: 2,
      documentation: { kind: "markdown", value: "prints" },
    },
    { startLineNumber: 1 },
  );
  expect(item).toMatchObject({
    label: "Println",
    kind: 1,
    insertText: "Println($0)",
    insertTextRules: 4,
    documentation: { value: "prints" },
    range: { startLineNumber: 1 },
  });
  const edit = toMonacoCompletion(
    m,
    {
      label: "a",
      textEdit: {
        range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } },
        newText: "abc",
      },
    },
    {},
  );
  expect(edit.insertText).toBe("abc");
  expect(edit.range.endColumn).toBe(2);
});

it("replies MethodNotFound to unknown requests and handles known ones", async () => {
  const { ws } = await connectGo();
  ws.push({ id: 90, method: "workspace/semanticTokens/refresh", params: {} });
  ws.push({
    id: 91,
    method: "workspace/configuration",
    params: { items: [{}, {}] },
  });
  await vi.waitFor(() => expect(ws.sent.some((m) => m.id === 91)).toBe(true));
  expect(ws.sent.find((m) => m.id === 90).error.code).toBe(-32601);
  expect(ws.sent.find((m) => m.id === 91).result).toEqual([null, null]);
});

it("tracks $/progress in lsp state", async () => {
  const { ws } = await connectGo();
  ws.push({ id: 5, method: "window/workDoneProgress/create", params: { token: "t" } });
  ws.push({
    method: "$/progress",
    params: { token: "t", value: { kind: "begin", title: "Indexing", percentage: 10 } },
  });
  await vi.waitFor(() => expect(lspState.gopls.progress?.title).toBe("Indexing"));
  ws.push({ method: "$/progress", params: { token: "t", value: { kind: "end" } } });
  await vi.waitFor(() => expect(lspState.gopls.progress).toBeNull());
});

it("close 4001 marks the server taken over; 4002 not found", async () => {
  const { ws } = await connectGo();
  ws.close(4001);
  expect(lspState.gopls.conn).toBe("taken_over");
  const ctx = setup({ "file:///work/b.ts": fakeModel("file:///work/b.ts", "typescript") });
  connectLanguageServers(ctx.store);
  const ws2 = FakeWS.all.at(-1)!;
  ws2.open();
  ws2.close(4002);
  expect(lspState.tsls.conn).toBe("not_found");
});

it("applies workspace/applyEdit to open models", async () => {
  const { ws, model } = await connectGo();
  ws.push({
    id: 7,
    method: "workspace/applyEdit",
    params: {
      edit: {
        changes: {
          "file:///work/a.go": [
            {
              range: { start: { line: 0, character: 0 }, end: { line: 0, character: 0 } },
              newText: "// hi\n",
            },
          ],
        },
      },
    },
  });
  await vi.waitFor(() => expect(ws.sent.some((m) => m.id === 7)).toBe(true));
  expect(ws.sent.find((m) => m.id === 7).result).toEqual({ applied: true });
  expect(model.pushEditOperations.mock.calls[0][1][0].text).toBe("// hi\n");
});

it("dispose closes the socket and clears diagnostics", async () => {
  const { ws, dispose, store } = await connectGo();
  ws.push({
    method: "textDocument/publishDiagnostics",
    params: {
      uri: "file:///work/a.go",
      diagnostics: [
        { range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } }, message: "m" },
      ],
    },
  });
  await vi.waitFor(() => expect(store.diagnostics.size).toBe(1));
  dispose();
  expect(ws.readyState).toBe(3);
  expect(store.diagnostics.size).toBe(0);
  expect(lspState.gopls.conn).toBe("idle");
});
