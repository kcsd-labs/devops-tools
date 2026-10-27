import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useApi } from "../useApi";
import { useDialogs } from "../components/Dialogs";
import { fromPostman, toPostman } from "../postman";
import type {
  APIEndpoint,
  Header,
  RequestCollection,
  RequestResponse,
  SavedRequest,
  ServiceInfo,
} from "../api";

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
const READ_ONLY = new Set(["GET", "HEAD", "OPTIONS"]);
const HISTORY_LIMIT = 20;

/** What this browser sent lately: method, path and status only. Bodies and
    headers stay out — they are where tokens and personal data travel, and
    this is kept in plain browser storage. */
interface Sent {
  method: string;
  port: number;
  path: string;
  status?: number;
  at: number;
}

function historyKey(ns: string, svc: string) {
  return `devops-tools.requests.${ns}/${svc}`;
}

function readHistory(ns: string, svc: string): Sent[] {
  try {
    return JSON.parse(localStorage.getItem(historyKey(ns, svc)) ?? "[]") as Sent[];
  } catch {
    return [];
  }
}

function writeHistory(ns: string, svc: string, h: Sent[]) {
  try {
    localStorage.setItem(historyKey(ns, svc), JSON.stringify(h.slice(0, HISTORY_LIMIT)));
  } catch {
    // A convenience, nothing more.
  }
}

/** Header names whose values are credentials. Saving one in clear puts it in
    front of everyone who may open the service. */
const CREDENTIAL = /^(authorization|cookie|x-api-key|api-key|x-auth-token|x-access-token|proxy-authorization)$/i;

function clearCredentials(headers: Header[]): string[] {
  return headers
    .filter((h) => h.value.trim() && !h.value.includes("{{secret:"))
    .filter((h) => CREDENTIAL.test(h.name.trim()) || /^bearer\s+\S{12,}/i.test(h.value.trim()))
    .map((h) => h.name);
}

function blankDraft(): SavedRequest {
  return { name: "", folder: "", method: "GET", path: "/", headers: [], body: "" };
}

function sizeText(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** The body as it is best read: indented when it is JSON. */
function prettyBody(r: RequestResponse): string {
  if (!r.body) return "";
  if (r.base64) return "(binary — " + sizeText(r.size) + ")";
  try {
    return JSON.stringify(JSON.parse(r.body), null, 2);
  } catch {
    return r.body;
  }
}

function shellQuote(s: string): string {
  return "'" + s.replace(/'/g, `'\\''`) + "'";
}

/** The same request as a curl command, to run from a pod. Secret references
    stay as they are: the command is for reading and copying, and the value is
    not this page's to print. */
function curlOf(method: string, url: string, headers: Header[], body: string): string {
  const parts = ["curl"];
  if (method !== "GET") parts.push("-X " + method);
  parts.push(shellQuote(url));
  for (const h of headers) if (h.name.trim()) parts.push("-H " + shellQuote(`${h.name}: ${h.value}`));
  if (body) parts.push("--data-raw " + shellQuote(body));
  return parts.join(" \\\n  ");
}

function statusTone(r: RequestResponse): string {
  if (!r.status) return "err";
  if (r.status < 300) return "ok";
  if (r.status < 400) return "muted";
  if (r.status < 500) return "warn";
  return "err";
}

export function Requests() {
  const api = useApi();
  const { namespace = "", name = "" } = useParams();
  const { dialogs, confirm, alertDlg, toast } = useDialogs();

  const [svc, setSvc] = useState<ServiceInfo | null>(null);
  const [domain, setDomain] = useState("cluster.local");
  const [port, setPort] = useState(0);
  const [coll, setColl] = useState<RequestCollection | null>(null);
  const [docs, setDocs] = useState<{ source?: string; endpoints: APIEndpoint[] } | null>(null);
  const [docsLoading, setDocsLoading] = useState(false);
  const [history, setHistory] = useState<Sent[]>(() => readHistory(namespace, name));
  const [draft, setDraft] = useState<SavedRequest>(blankDraft);
  const [dirty, setDirty] = useState(false);
  const [tab, setTab] = useState<"headers" | "body">("headers");
  const [response, setResponse] = useState<RequestResponse | null>(null);
  const [respTab, setRespTab] = useState<"body" | "headers" | "curl">("body");
  const [sending, setSending] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const fileInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    api
      .services(namespace)
      .then((l) => {
        const s = l.services.find((x) => x.name === name);
        if (!s) {
          setError(`There is no service ${name} in ${namespace}.`);
          return;
        }
        setSvc(s);
        setDomain(l.clusterDomain || "cluster.local");
        setPort(s.ports[0]?.port ?? 0);
      })
      .catch((e) => setError(String(e?.message ?? e)));
    api
      .requestCollection(namespace, name)
      .then(setColl)
      .catch((e) => setError(String(e?.message ?? e)));
  }, [api, namespace, name]);

  // The service's own description of its API, for the port in use. Quietly
  // empty when it has none: most services do not publish one.
  useEffect(() => {
    if (!port) return;
    let live = true;
    setDocsLoading(true);
    api
      .serviceApiDocs(namespace, name, port)
      .then((d) => live && setDocs(d))
      .catch(() => live && setDocs({ endpoints: [] }))
      .finally(() => live && setDocsLoading(false));
    return () => {
      live = false;
    };
  }, [api, namespace, name, port]);

  const host = svc ? `${svc.name}.${svc.namespace}:${port}` : "";
  const scheme = useMemo(() => {
    const p = svc?.ports.find((x) => x.port === port);
    const hint = (p?.appProtocol || p?.name || "").toLowerCase();
    return hint.startsWith("https") || (!hint && port === 443) ? "https" : "http";
  }, [svc, port]);
  const fullUrl = svc ? `${scheme}://${svc.name}.${svc.namespace}.svc.${domain}:${port}` : "";

  const mayWrite = !!coll?.mayWrite;
  const readOnly = !!coll?.collection.readOnly;
  const canSave = mayWrite && !readOnly;
  const writesBlocked = !READ_ONLY.has(draft.method) && !mayWrite;

  const update = (patch: Partial<SavedRequest>) => {
    setDraft((d) => ({ ...d, ...patch }));
    setDirty(true);
  };

  const load = async (r: SavedRequest) => {
    if (dirty && !(await confirm({ message: "Discard the changes to the request being edited?" }))) {
      return;
    }
    setDraft({ ...r, headers: r.headers ?? [], body: r.body ?? "", folder: r.folder ?? "" });
    if (r.port && svc?.ports.some((p) => p.port === r.port)) setPort(r.port);
    setDirty(false);
    setResponse(null);
  };

  const send = async () => {
    setSending(true);
    setError("");
    const headers = (draft.headers ?? []).filter((h) => h.name.trim());
    // A JSON body with no stated type is sent as JSON: forgetting the header
    // is the commonest reason a first request comes back 415.
    if (
      draft.body &&
      !headers.some((h) => h.name.toLowerCase() === "content-type") &&
      /^\s*[[{]/.test(draft.body)
    ) {
      headers.push({ name: "Content-Type", value: "application/json" });
    }
    try {
      const r = await api.sendRequest(namespace, name, {
        method: draft.method,
        port,
        path: draft.path,
        headers,
        body: draft.body ?? "",
      });
      setResponse(r);
      setRespTab("body");
      const h = [
        { method: draft.method, port, path: draft.path, status: r.status, at: Date.now() },
        ...history,
      ].slice(0, HISTORY_LIMIT);
      setHistory(h);
      writeHistory(namespace, name, h);
    } catch (e) {
      setError(String((e as Error)?.message ?? e));
    } finally {
      setSending(false);
    }
  };

  const save = async () => {
    if (!coll) return;
    if (!draft.name?.trim()) {
      await alertDlg("Give the request a name first — it is how it is found in the list.");
      return;
    }
    const exposed = clearCredentials(draft.headers ?? []);
    if (
      exposed.length > 0 &&
      !(await confirm({
        title: "A credential in clear",
        message:
          `${exposed.join(", ")} looks like a credential. Saved requests are visible to everyone ` +
          "who may open this service. Put the value in a secret and write {{secret:name/key}} " +
          "instead — it is filled in only when someone allowed to read the secret sends. Save as it is anyway?",
        okText: "Save anyway",
        danger: true,
      }))
    ) {
      return;
    }
    setSaving(true);
    try {
      const c = await api.saveRequests(namespace, name, {
        version: coll.collection.version,
        save: [{ ...draft, port: port === svc?.ports[0]?.port ? 0 : port }],
      });
      setColl({ ...coll, collection: c });
      // A new request has its id now: find it, so the next save replaces it.
      const mine = c.requests.find(
        (r) => r.id === draft.id || (!draft.id && r.name === draft.name.trim() && r.path === draft.path)
      );
      if (mine) setDraft((d) => ({ ...d, id: mine.id }));
      setDirty(false);
      toast("Saved");
    } catch (e) {
      setError(String((e as Error)?.message ?? e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (r: SavedRequest) => {
    if (!coll || !r.id) return;
    if (!(await confirm({ message: `Remove “${r.name}” from the saved requests?`, danger: true, okText: "Remove" }))) {
      return;
    }
    try {
      const c = await api.saveRequests(namespace, name, { version: coll.collection.version, remove: [r.id] });
      setColl({ ...coll, collection: c });
      if (draft.id === r.id) setDraft((d) => ({ ...d, id: undefined }));
    } catch (e) {
      setError(String((e as Error)?.message ?? e));
    }
  };

  const exportCollection = () => {
    if (!coll || !svc) return;
    const text = toPostman(
      `${svc.name} (${svc.namespace})`,
      `${scheme}://${svc.name}.${svc.namespace}:${svc.ports[0]?.port ?? port}`,
      svc.ports[0]?.port ?? 0,
      coll.collection.requests
    );
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], { type: "application/json" }));
    a.download = `${svc.name}.postman_collection.json`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  const importCollection = async (file: File) => {
    if (!coll) return;
    try {
      const { requests, skipped } = fromPostman(await file.text());
      if (requests.length === 0) {
        await alertDlg("The file holds no requests.");
        return;
      }
      const note = skipped ? ` ${skipped} with form or file bodies come across without the body.` : "";
      if (!(await confirm({ message: `Add ${requests.length} requests to the saved ones?${note}`, okText: "Import" }))) {
        return;
      }
      const c = await api.saveRequests(namespace, name, { version: coll.collection.version, save: requests });
      setColl({ ...coll, collection: c });
      toast(`Imported ${requests.length} requests`);
    } catch (e) {
      setError(String((e as Error)?.message ?? e));
    }
  };

  // Saved requests by folder, folders first in name order.
  const folders = useMemo(() => {
    const m = new Map<string, SavedRequest[]>();
    for (const r of coll?.collection.requests ?? []) {
      const f = r.folder || "";
      m.set(f, [...(m.get(f) ?? []), r]);
    }
    return [...m].sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)));
  }, [coll]);

  const tags = useMemo(() => {
    const m = new Map<string, APIEndpoint[]>();
    for (const e of docs?.endpoints ?? []) m.set(e.tag || "other", [...(m.get(e.tag || "other") ?? []), e]);
    return [...m];
  }, [docs]);

  const headers = draft.headers ?? [];
  const setHeader = (i: number, patch: Partial<Header>) =>
    update({ headers: headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) });

  return (
    <div className="page">
      {dialogs}
      <div className="page-head">
        <div>
          <Link to={`/services?namespace=${namespace}`} className="crumb">
            ← Services · {namespace}
          </Link>
          <h1 className="page-title">{name}</h1>
          {svc && (
            <div className="muted mono req-sub">
              {host}
              {svc.endpoints && ` · ${svc.endpoints.ready} / ${svc.endpoints.total} pods ready`}
            </div>
          )}
        </div>
        {svc && svc.ports.length > 1 && (
          <select
            className="input"
            style={{ flex: "0 0 auto", width: 200 }}
            value={port}
            onChange={(e) => setPort(Number(e.target.value))}
            aria-label="Port"
          >
            {svc.ports.map((p) => (
              <option key={p.port} value={p.port}>
                {(p.name ? p.name + " " : "") + p.port}
              </option>
            ))}
          </select>
        )}
      </div>

      {error && (
        <div className="alert" onClick={() => setError("")} title="Click to dismiss">
          {error}
        </div>
      )}

      <div className="req-layout">
        <aside className="card no-pad req-side">
          <div className="req-side-head">
            <span>Saved</span>
            <span className="req-side-actions">
              {canSave && (
                <button
                  className="link-btn"
                  onClick={() => fileInput.current?.click()}
                  title="Add requests from a Postman collection, or a file exported from another environment"
                >
                  Import
                </button>
              )}
              {(coll?.collection.requests.length ?? 0) > 0 && (
                <button className="link-btn" onClick={exportCollection} title="As a Postman collection">
                  Export
                </button>
              )}
              <button
                className="link-btn"
                onClick={async () => {
                  if (dirty && !(await confirm({ message: "Discard the changes to the request being edited?" }))) return;
                  setDraft(blankDraft());
                  setDirty(false);
                  setResponse(null);
                }}
              >
                + New
              </button>
            </span>
            <input
              ref={fileInput}
              type="file"
              accept=".json,application/json"
              hidden
              onChange={(e) => {
                const f = e.target.files?.[0];
                e.target.value = "";
                if (f) importCollection(f);
              }}
            />
          </div>
          {readOnly && (
            <div className="req-side-note">
              Managed by {coll?.collection.managedBy || "something outside the portal"} — read only here.
            </div>
          )}
          {coll && coll.collection.requests.length === 0 && (
            <div className="req-side-note">
              Nothing saved yet.{canSave ? " Save a request, or import a Postman collection." : ""}
            </div>
          )}
          {folders.map(([folder, rs]) => (
            <div key={"f/" + folder}>
              {folder && <div className="req-folder">{folder}</div>}
              {rs.map((r) => (
                <div
                  key={r.id}
                  className={"req-item" + (draft.id === r.id ? " on" : "")}
                  onClick={() => load(r)}
                  title={r.path}
                >
                  <span className={"req-m m-" + r.method}>{r.method}</span>
                  <span className="req-item-name">{r.name}</span>
                  {canSave && (
                    <button
                      className="req-x"
                      title="Remove"
                      onClick={(e) => {
                        e.stopPropagation();
                        remove(r);
                      }}
                    >
                      ×
                    </button>
                  )}
                </div>
              ))}
            </div>
          ))}

          <div className="req-side-head sub">
            <span>From the service</span>
            <span className="req-side-src">{docsLoading ? "looking…" : docs?.source ?? ""}</span>
          </div>
          {!docsLoading && docs && docs.endpoints.length === 0 && (
            <div className="req-side-note">No OpenAPI document at /v3/api-docs or the other usual paths.</div>
          )}
          {tags.map(([tag, eps]) => (
            <div key={"t/" + tag}>
              <div
                className="req-folder clickable"
                onClick={() => setOpen((o) => ({ ...o, [tag]: !o[tag] }))}
              >
                {open[tag] ? "▾" : "▸"} {tag} <span className="muted">· {eps.length}</span>
              </div>
              {open[tag] &&
                eps.map((e) => (
                  <div
                    key={e.method + e.path}
                    className="req-item"
                    title={e.summary}
                    onClick={() =>
                      load({ name: e.summary || e.path, folder: e.tag, method: e.method, path: e.path })
                    }
                  >
                    <span className={"req-m m-" + e.method}>{e.method}</span>
                    <span className="req-item-name mono">{e.path}</span>
                  </div>
                ))}
            </div>
          ))}

          {history.length > 0 && (
            <>
              <div className="req-side-head sub">
                <span>History</span>
                <span className="req-side-src">this browser</span>
              </div>
              {history.map((h, i) => (
                <div
                  key={i}
                  className="req-item"
                  onClick={() => load({ name: "", method: h.method, path: h.path, port: h.port })}
                  title={new Date(h.at).toLocaleString()}
                >
                  <span className={"req-m m-" + h.method}>{h.method}</span>
                  <span className="req-item-name mono">{h.path}</span>
                  <span className={"req-st " + (h.status ? (h.status < 400 ? "ok" : h.status < 500 ? "warn" : "err") : "err")}>
                    {h.status ?? "✕"}
                  </span>
                </div>
              ))}
            </>
          )}
        </aside>

        <section className="req-main">
          <div className="card no-pad">
            <div className="req-meta">
              <input
                className="input sm"
                placeholder="Request name"
                value={draft.name}
                onChange={(e) => update({ name: e.target.value })}
              />
              <input
                className="input sm"
                placeholder="Folder"
                value={draft.folder ?? ""}
                onChange={(e) => update({ folder: e.target.value })}
              />
              {dirty && draft.id && <span className="muted req-dirty">unsaved changes</span>}
            </div>
            <div className="req-bar">
              <select
                className={"input req-method m-" + draft.method}
                value={draft.method}
                onChange={(e) => update({ method: e.target.value })}
                aria-label="Method"
              >
                {METHODS.map((m) => (
                  <option key={m} value={m} disabled={!READ_ONLY.has(m) && !mayWrite}>
                    {m}
                  </option>
                ))}
              </select>
              <div className="input req-url">
                <span className="req-host">{scheme}://{host}</span>
                <input
                  value={draft.path}
                  spellCheck={false}
                  onChange={(e) => update({ path: e.target.value })}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !sending && !writesBlocked) send();
                  }}
                  aria-label="Path"
                />
              </div>
              {canSave && (
                <button className="btn ghost" onClick={save} disabled={saving}>
                  {saving ? "Saving…" : "Save"}
                </button>
              )}
              <button
                className="btn"
                onClick={send}
                disabled={sending || !svc || writesBlocked}
                title={writesBlocked ? "Sending " + draft.method + " needs service-request-write" : undefined}
              >
                {sending ? "Sending…" : "Send"}
              </button>
            </div>
            <div className="tabs req-tabs">
              <button className={"tab" + (tab === "headers" ? " active" : "")} onClick={() => setTab("headers")}>
                Headers {headers.length > 0 && <span className="muted">{headers.length}</span>}
              </button>
              <button className={"tab" + (tab === "body" ? " active" : "")} onClick={() => setTab("body")}>
                Body {draft.body && <span className="muted">·</span>}
              </button>
            </div>
            {tab === "headers" ? (
              <div className="req-headers">
                {headers.map((h, i) => (
                  <div className="req-header-row" key={i}>
                    <input
                      className="input sm mono"
                      placeholder="Name"
                      value={h.name}
                      onChange={(e) => setHeader(i, { name: e.target.value })}
                    />
                    <input
                      className="input sm mono"
                      placeholder="Value"
                      value={h.value}
                      onChange={(e) => setHeader(i, { value: e.target.value })}
                    />
                    <button
                      className="req-x"
                      title="Remove"
                      onClick={() => update({ headers: headers.filter((_, j) => j !== i) })}
                    >
                      ×
                    </button>
                  </div>
                ))}
                <div className="req-hint">
                  <button className="link-btn" onClick={() => update({ headers: [...headers, { name: "", value: "" }] })}>
                    + Add header
                  </button>
                  <span className="muted">
                    A token or password goes in a secret: <span className="mono">{"{{secret:name/key}}"}</span>
                    {coll && !coll.maySecrets && " — you cannot use secrets in this namespace"}
                  </span>
                </div>
              </div>
            ) : (
              <textarea
                className="req-body mono"
                spellCheck={false}
                placeholder={READ_ONLY.has(draft.method) ? "A GET has no body, usually." : "{ }"}
                value={draft.body ?? ""}
                onChange={(e) => update({ body: e.target.value })}
              />
            )}
          </div>

          {response && (
            <div className="card no-pad req-resp">
              <div className="req-resp-head">
                <span className={"req-status " + statusTone(response)}>
                  {response.status ? `${response.status} ${response.statusText ?? ""}` : "No answer"}
                </span>
                <span className="muted">{response.ms} ms</span>
                {response.status ? <span className="muted">{sizeText(response.size)}</span> : null}
                <span className="req-resp-tabs">
                  {(["body", "headers", "curl"] as const).map((t) => (
                    <button
                      key={t}
                      className={"tab" + (respTab === t ? " active" : "")}
                      onClick={() => setRespTab(t)}
                    >
                      {t === "curl" ? "curl" : t[0].toUpperCase() + t.slice(1)}
                    </button>
                  ))}
                </span>
              </div>
              {response.error && <div className="req-resp-error">{response.error}</div>}
              {response.truncated && (
                <div className="req-resp-note">
                  Showing the first {sizeText(1 << 20)} of {sizeText(response.size)}.
                </div>
              )}
              {respTab === "body" && <pre className="console req-pre">{prettyBody(response) || " "}</pre>}
              {respTab === "headers" && (
                <table className="table req-resp-headers">
                  <tbody>
                    {(response.headers ?? []).map((h, i) => (
                      <tr key={i}>
                        <td className="mono muted">{h.name}</td>
                        <td className="mono">{h.value}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {respTab === "curl" && (
                <div style={{ position: "relative" }}>
                  <pre className="console req-pre">
                    {curlOf(draft.method, fullUrl + draft.path, headers, draft.body ?? "")}
                  </pre>
                  <button
                    className="btn ghost sm req-copy"
                    onClick={() => {
                      navigator.clipboard
                        ?.writeText(curlOf(draft.method, fullUrl + draft.path, headers, draft.body ?? ""))
                        .then(() => toast("Copied"))
                        .catch(() => setError("The browser refused to copy."));
                    }}
                  >
                    Copy
                  </button>
                </div>
              )}
              <div className="req-foot">
                Sent from the portal's pod. The audit trail records the method, port, path and status —
                never the query, the headers or either body.
              </div>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
