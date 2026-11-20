export type Monaco = typeof import("monaco-editor/editor/editor.api");
export type ITextModel =
  import("monaco-editor/editor/editor.api").editor.ITextModel;
export type ICodeEditor =
  import("monaco-editor/editor/editor.api").editor.IStandaloneCodeEditor;

const BY_EXT: Record<string, string> = {
  go: "go",
  ts: "typescript",
  tsx: "typescript",
  mts: "typescript",
  cts: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  json: "json",
  jsonc: "json",
  md: "markdown",
  markdown: "markdown",
  css: "css",
  scss: "scss",
  less: "less",
  html: "html",
  htm: "html",
  vue: "html",
  yaml: "yaml",
  yml: "yaml",
  sh: "shell",
  bash: "shell",
  zsh: "shell",
  sql: "sql",
  py: "python",
  rs: "rust",
  xml: "xml",
  toml: "ini",
  ini: "ini",
  proto: "protobuf",
};

export function languageOf(path: string): string {
  const name = path.split("/").pop() ?? path;
  if (name === "Dockerfile") return "dockerfile";
  const dot = name.lastIndexOf(".");
  return dot > 0
    ? (BY_EXT[name.slice(dot + 1).toLowerCase()] ?? "plaintext")
    : "plaintext";
}

const LABELS: Record<string, string> = {
  go: "Go",
  typescript: "TypeScript",
  javascript: "JavaScript",
  json: "JSON",
  markdown: "Markdown",
  css: "CSS",
  html: "HTML",
  yaml: "YAML",
  shell: "Shell",
  plaintext: "Plain text",
};
export const languageLabel = (id: string) =>
  LABELS[id] ?? id.charAt(0).toUpperCase() + id.slice(1);

function hex(v: string, fallback: string): string {
  const s = v.trim();
  const m = /^#([0-9a-f]{3})$/i.exec(s);
  if (m) return `#${[...m[1]].map((c) => c + c).join("")}`;
  return /^#[0-9a-f]{6}([0-9a-f]{2})?$/i.test(s) ? s : fallback;
}
const bare = (c: string) => c.replace("#", "");

function defineTheme(monaco: Monaco) {
  const css = getComputedStyle(document.documentElement);
  const v = (name: string, fb: string) => hex(css.getPropertyValue(name), fb);
  const dark = document.documentElement.dataset.theme === "dark";
  const bg = v("--surface", dark ? "#0b2b26" : "#ffffff");
  const ink = v("--ink", dark ? "#daf1de" : "#051f20");
  const muted = v("--muted", "#4a6660");
  const line = v("--line", "#d7e2da");
  const accent = v("--accent", "#235347");
  const info = v("--info", "#2a627b");
  const warn = v("--warning", "#8a4f0c");
  const ok = v("--success", "#1d6e4a");
  const bad = v("--danger", "#a83446");
  const tint = v("--tint", "#daf1de");
  monaco.editor.defineTheme("legos", {
    base: dark ? "vs-dark" : "vs",
    inherit: true,
    rules: [
      { token: "comment", foreground: bare(muted), fontStyle: "italic" },
      { token: "keyword", foreground: bare(accent), fontStyle: "bold" },
      { token: "string", foreground: bare(ok) },
      { token: "number", foreground: bare(warn) },
      { token: "type", foreground: bare(info) },
      { token: "delimiter", foreground: bare(muted) },
      { token: "tag", foreground: bare(accent) },
      { token: "attribute.name", foreground: bare(info) },
      { token: "regexp", foreground: bare(bad) },
    ],
    colors: {
      "editor.background": bg,
      "editor.foreground": ink,
      "editorLineNumber.foreground": muted,
      "editorLineNumber.activeForeground": ink,
      "editor.lineHighlightBackground": v("--surface-2", "#e9f0eb") + "80",
      "editor.selectionBackground": tint,
      "editorCursor.foreground": accent,
      "editorWidget.background": bg,
      "editorWidget.border": line,
      "editorGutter.background": bg,
      "editorIndentGuide.background1": line,
      "scrollbarSlider.background": line + "99",
    },
  });
  monaco.editor.setTheme("legos");
}

let loading: Promise<Monaco> | null = null;
let observer: MutationObserver | null = null;

export function loadMonaco(): Promise<Monaco> {
  loading ??= (async () => {
    // Contributions register their services; they must evaluate before editor.api builds the service collection.
    await import("./monacoContrib");
    const [monaco, workerMod] = await Promise.all([
      import("monaco-editor/editor/editor.api"),
      import("monaco-editor/editor/editor.worker?worker"),
      import("./monacoLanguages"),
    ]);
    const EditorWorker = workerMod.default;
    (self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = {
      getWorker: () => new EditorWorker(),
    };
    defineTheme(monaco);
    observer = new MutationObserver(() => defineTheme(monaco));
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    return monaco;
  })();
  loading.catch(() => (loading = null));
  return loading;
}

export function disposeMonacoObservers() {
  observer?.disconnect();
  observer = null;
}
