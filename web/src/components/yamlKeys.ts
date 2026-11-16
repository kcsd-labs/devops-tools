import { hoverTooltip, type Extension, type Text } from "@uiw/react-codemirror";
import { yamlLanguage } from "@codemirror/lang-yaml";
import type { CompletionContext, CompletionResult } from "@codemirror/autocomplete";

// The chart's own words about its keys, where the keys are written: on
// hover, and as completions while typing one. Paths are read from block YAML
// by indentation — what values files are written in; keys inside lists are
// left alone, the charts rarely document those.

const KEY = /^(\s*)(- )?("[^"]*"|'[^']*'|[^\s#:'"-][^:#]*?|-[^\s:#][^:#]*?):(\s|$)/;

type Key = { indent: number; key: string; list: boolean; at: number };

function keyOf(line: string): Key | null {
  const m = KEY.exec(line);
  if (!m) return null;
  const list = !!m[2];
  return {
    indent: m[1].length + (list ? 2 : 0),
    key: m[3].replace(/^["']|["']$/g, ""),
    list,
    at: m[1].length + (list ? 2 : 0),
  };
}

const indentOf = (s: string) => s.length - s.trimStart().length;

/** The keys above a line at this indentation: a.b for "a:\n  b:\n    <here>". */
function parentPath(doc: Text, lineNo: number, indent: number): string[] | null {
  const path: string[] = [];
  for (let n = lineNo - 1; n >= 1 && indent > 0; n--) {
    const t = doc.line(n).text;
    if (!t.trim() || t.trim().startsWith("#")) continue;
    const ind = indentOf(t);
    if (ind >= indent) continue;
    const k = keyOf(t);
    if (!k || k.list) return null;
    path.unshift(k.key);
    indent = ind;
  }
  return path;
}

/** The full path of the key on a line, or null when it is not a plain key. */
function pathAt(doc: Text, lineNo: number): string[] | null {
  const own = keyOf(doc.line(lineNo).text);
  if (!own || own.list) return null;
  const parent = parentPath(doc, lineNo, own.indent);
  return parent ? [...parent, own.key] : null;
}

/** On hover over a key: what the chart says about it. */
export function keyDocs(docs: Record<string, string>): Extension {
  return hoverTooltip((view, pos) => {
    const line = view.state.doc.lineAt(pos);
    const k = keyOf(line.text);
    if (!k || k.list) return null;
    const from = line.from + k.at;
    const to = from + k.key.length;
    if (pos < from || pos > to) return null;
    const path = pathAt(view.state.doc, line.number);
    if (!path) return null;
    const p = path.join(".");
    const said = docs[p];
    return {
      pos: from,
      end: to,
      above: true,
      create() {
        const dom = document.createElement("div");
        dom.className = "cm-keydoc";
        const head = document.createElement("div");
        head.className = "cm-keydoc-path";
        head.textContent = p;
        dom.appendChild(head);
        const body = document.createElement("div");
        body.className = said ? "cm-keydoc-text" : "cm-keydoc-text muted";
        body.textContent =
          said === undefined
            ? "Not in the chart's values.yaml — check the spelling, or the chart may take it free-form."
            : said || "The chart says nothing about this key; Chart defaults shows its value.";
        dom.appendChild(body);
        return { dom };
      },
    };
  });
}

/** While typing a key: the keys the chart has at that place, described. */
export function keyCompletion(docs: Record<string, string>): Extension {
  const children = new Map<string, string[]>();
  for (const k of Object.keys(docs)) {
    const i = k.lastIndexOf(".");
    const parent = i < 0 ? "" : k.slice(0, i);
    const list = children.get(parent) ?? [];
    list.push(k.slice(i + 1));
    children.set(parent, list);
  }
  return yamlLanguage.data.of({
    autocomplete: (ctx: CompletionContext): CompletionResult | null => {
      const line = ctx.state.doc.lineAt(ctx.pos);
      const before = line.text.slice(0, ctx.pos - line.from);
      const m = /^(\s*)([\w.-]*)$/.exec(before);
      if (!m || (!ctx.explicit && m[2] === "")) return null;
      const parent = parentPath(ctx.state.doc, line.number, m[1].length);
      if (!parent) return null;
      const p = parent.join(".");
      const options = (children.get(p) ?? []).map((k) => {
        const said = docs[p ? `${p}.${k}` : k];
        return { label: k, type: "property", info: said || undefined, apply: `${k}: ` };
      });
      if (options.length === 0) return null;
      return { from: ctx.pos - m[2].length, options, validFor: /^[\w.-]*$/ };
    },
  });
}
