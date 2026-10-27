// Moving saved requests between environments, and in from Postman: a
// collection is exported in Postman's own format (v2.1), so the same file
// opens in Postman and imports into another environment's portal.

import type { Header, SavedRequest } from "./api";

const SCHEMA = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json";

type PostmanUrl =
  | string
  | {
      raw?: string;
      path?: (string | { value?: string })[];
      query?: { key?: string; value?: string; disabled?: boolean }[];
    };

interface PostmanItem {
  name?: string;
  item?: PostmanItem[];
  request?: {
    method?: string;
    header?: { key?: string; value?: string; disabled?: boolean }[];
    url?: PostmanUrl;
    body?: { mode?: string; raw?: string };
  };
}

/** A collection as a Postman v2.1 file. The service's address is a variable,
    baseUrl, so the file works against the same service anywhere — which is
    the point of moving it. */
export function toPostman(
  title: string,
  baseUrl: string,
  defaultPort: number,
  requests: SavedRequest[]
): string {
  const one = (r: SavedRequest) => {
    // A request on another port of the service cannot use baseUrl, which
    // carries the first port. It keeps its own address.
    const host =
      !r.port || r.port === defaultPort ? "{{baseUrl}}" : baseUrl.replace(/:\d+$/, `:${r.port}`);
    const req: Record<string, unknown> = {
      method: r.method,
      header: (r.headers ?? []).map((h) => ({ key: h.name, value: h.value })),
      url: { raw: host + r.path },
    };
    if (r.body) req.body = { mode: "raw", raw: r.body };
    return { name: r.name, request: req };
  };

  const folders = new Map<string, SavedRequest[]>();
  const loose: SavedRequest[] = [];
  for (const r of requests) {
    if (!r.folder) loose.push(r);
    else folders.set(r.folder, [...(folders.get(r.folder) ?? []), r]);
  }
  const item = [
    ...[...folders].map(([name, rs]) => ({ name, item: rs.map(one) })),
    ...loose.map(one),
  ];
  return JSON.stringify(
    { info: { name: title, schema: SCHEMA }, variable: [{ key: "baseUrl", value: baseUrl }], item },
    null,
    2
  );
}

/** The path and query of a Postman URL, wherever it was meant to point. The
    host is dropped: an imported request always goes to the service it is
    imported into. */
function pathOf(url: PostmanUrl | undefined): string {
  let raw = "";
  if (typeof url === "string") raw = url;
  else if (url?.raw) raw = url.raw;
  else if (url?.path) {
    const segs = url.path.map((p) => (typeof p === "string" ? p : (p.value ?? "")));
    const q = (url.query ?? []).filter((p) => !p.disabled).map((p) => `${p.key}=${p.value ?? ""}`);
    raw = "/" + segs.join("/") + (q.length ? "?" + q.join("&") : "");
  }
  raw = raw.trim();
  // {{baseUrl}}/x, {{host}}:8080/x
  raw = raw.replace(/^\{\{[^}]+\}\}(:\d+)?/, "");
  // http://anything/x
  raw = raw.replace(/^[a-z][a-z0-9+.-]*:\/\/[^/?#]*/i, "");
  if (!raw.startsWith("/")) raw = "/" + raw;
  return raw.replace(/#.*$/, "");
}

/** Reads a Postman collection (v2.0 or v2.1) into requests ready to save.
    Folders nest in Postman and are flattened here, their names joined. Only
    raw bodies come across; form and file bodies are counted as skipped. */
export function fromPostman(text: string): { requests: SavedRequest[]; skipped: number } {
  const doc = JSON.parse(text) as { item?: PostmanItem[] };
  if (!Array.isArray(doc.item)) throw new Error("This is not a Postman collection.");
  const out: SavedRequest[] = [];
  let skipped = 0;

  const walk = (items: PostmanItem[], folder: string[]) => {
    for (const it of items) {
      if (it.item) {
        walk(it.item, [...folder, it.name ?? ""]);
        continue;
      }
      const r = it.request;
      if (!r) continue;
      const mode = r.body?.mode;
      if (mode && mode !== "raw") skipped++;
      const headers: Header[] = (r.header ?? [])
        .filter((h) => !h.disabled && h.key)
        .map((h) => ({ name: h.key!, value: h.value ?? "" }));
      out.push({
        folder: folder.filter(Boolean).join(" / ") || undefined,
        name: (it.name ?? "").trim() || "Untitled",
        method: (r.method ?? "GET").toUpperCase(),
        path: pathOf(r.url),
        headers,
        body: mode === "raw" ? (r.body?.raw ?? "") : "",
      });
    }
  };
  walk(doc.item, []);
  return { requests: out, skipped };
}
