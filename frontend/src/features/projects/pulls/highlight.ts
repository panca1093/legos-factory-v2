import { languageOf, loadMonaco } from "../../editor/monaco";
import type { DiffHunk } from "./diff";

export type Seg = { t: string; c: string };
type Tok = { offset: number; type: string };

const KINDS = new Set([
  "keyword",
  "string",
  "number",
  "comment",
  "type",
  "tag",
  "attribute",
  "regexp",
  "predefined",
  "constant",
]);

export function segments(text: string, toks: Tok[]): Seg[] {
  if (!toks.length) return [{ t: text, c: "" }];
  return toks
    .map((k, i) => {
      const kind = k.type.split(".")[0];
      return {
        t: text.slice(k.offset, toks[i + 1]?.offset ?? text.length),
        c: KINDS.has(kind) ? `tk-${kind}` : "",
      };
    })
    .filter((g) => g.t);
}

// Each side of a hunk is tokenized as one text, so strings and comments spanning lines keep their colour.
export async function highlightHunks(
  path: string,
  hunks: DiffHunk[],
): Promise<Seg[][][] | null> {
  const lang = languageOf(path);
  if (lang === "plaintext" || !hunks.length) return null;
  try {
    const monaco = await loadMonaco();
    await monaco.editor.colorize("", lang, {});
    return hunks.map((h) => {
      const side = (keep: (t: string) => boolean) => {
        const lines = h.lines.filter((l) => keep(l.t));
        const toks = monaco.editor.tokenize(
          lines.map((l) => l.s).join("\n"),
          lang,
        );
        return new Map(lines.map((l, i) => [l, segments(l.s, toks[i] ?? [])]));
      };
      const now = side((t) => t !== "del"),
        was = side((t) => t !== "add");
      return h.lines.map(
        (l) => (l.t === "del" ? was : now).get(l) ?? [{ t: l.s, c: "" }],
      );
    });
  } catch {
    return null;
  }
}
