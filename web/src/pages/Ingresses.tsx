import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import CodeMirror from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { useEditorTheme } from "../editorTheme";
import { useApi } from "../useApi";
import { useDialogs } from "../components/Dialogs";
import { ExposeDialog, SettingsEditor, type Setting } from "../components/ExposeDialog";
import type { IngressCert, IngressDoc, IngressForm, IngressList, IngressProblem, IngressRoute } from "../api";
import { checkSummary, routeURL, worst } from "../ingress";

// How each service is reached from outside, and why not when it is not. Every
// problem is shown with what to do about it, and the simple fixes are one
// press. Nothing here refuses a host or a setting: what a person may do is
// what their role grants.

const NAMESPACE_KEY = "devops-tools.ingressNamespace";

function remembered(): string {
  try {
    return localStorage.getItem(NAMESPACE_KEY) ?? "";
  } catch {
    return "";
  }
}

export function Ingresses() {
  const api = useApi();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const [namespaces, setNamespaces] = useState<string[] | null>(null);
  const [data, setData] = useState<IngressList | null>(null);
  const [services, setServices] = useState<{ name: string; ports: string[] }[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [onlyProblems, setOnlyProblems] = useState(false);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState("");
  const { dialogs, confirm, toast } = useDialogs();

  const asked = params.get("namespace") ?? "";
  const namespace =
    namespaces && namespaces.includes(asked)
      ? asked
      : namespaces?.includes(remembered())
        ? remembered()
        : (namespaces?.[0] ?? "");
  const editing = params.get("name") ?? "";

  const apiRef = useRef(api);
  apiRef.current = api;

  useEffect(() => {
    api
      .ingressNamespaces()
      .then((r) => setNamespaces(r.namespaces))
      .catch((e) => setError(e.message));
  }, [api]);

  const load = useCallback(() => {
    if (!namespace) return;
    try {
      localStorage.setItem(NAMESPACE_KEY, namespace);
    } catch {
      // A remembered preference, nothing more.
    }
    setLoading(true);
    setError("");
    apiRef.current
      .ingresses(namespace)
      // A route with no list of problems is one with none.
      .then((d) => setData({ ...d, routes: (d.routes ?? []).map((r) => ({ ...r, problems: r.problems ?? [] })) }))
      .catch((e) => {
        setData(null);
        setError(e.message);
      })
      .finally(() => setLoading(false));
    // The services, for the pickers. A role without service-list still edits:
    // the fields take free text then.
    apiRef.current
      .services(namespace)
      .then((l) => setServices(l.services.map((s) => ({ name: s.name, ports: s.ports.map((p) => String(p.port)) }))))
      .catch(() => setServices([]));
  }, [namespace]);

  useEffect(load, [load]);

  const open = (name: string, ns = namespace) =>
    setParams(name ? { namespace: ns, name } : { namespace: ns }, { replace: !name });

  const matching = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data?.routes ?? []).filter(
      (r) => !q || [r.host, r.path, r.service, r.ingress].some((x) => x.toLowerCase().includes(q))
    );
  }, [data, query]);
  const problemCount = matching.filter((r) => r.problems.length > 0).length;
  const shown = onlyProblems ? matching.filter((r) => r.problems.length > 0) : matching;

  // The one-press fixes: the same save the editor makes, of one field.
  const applyFix = async (r: IngressRoute, p: IngressProblem) => {
    const fix = p.fix!;
    if (fix.kind === "pods") {
      nav(`/ns/${namespace}/pods`);
      return;
    }
    if (fix.kind === "ingress") {
      // Another team's namespace is named, but only opened for someone who
      // may look at it.
      if (fix.namespace === namespace || namespaces?.includes(fix.namespace!)) open(fix.name!, fix.namespace);
      else toast(`${fix.name} is in ${fix.namespace}, which your roles do not reach — ask its team`);
      return;
    }
    setBusy(r.ingress + r.host + r.path);
    try {
      const doc = await api.ingress(namespace, r.ingress);
      const form: IngressForm = { class: doc.class, rules: doc.rules, tls: doc.tls, annotations: doc.annotations };
      let what = "";
      if (fix.kind === "port") {
        form.rules = form.rules.map((x) =>
          x.host === r.host && (x.path || "/") === r.path ? { ...x, port: fix.value! } : x
        );
        what = `${r.host}${r.path} now goes to ${r.service}:${fix.value}`;
      } else if (fix.kind === "tls") {
        form.tls = form.tls.map((t) => (t.hosts.includes(r.host) ? { ...t, secret: fix.value! } : t));
        what = `${r.host} now uses ${fix.value}`;
      }
      await api.updateIngress(namespace, r.ingress, doc.version, { form });
      toast(what);
      load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy("");
    }
  };

  const fixLabel = (p: IngressProblem): string => {
    switch (p.fix?.kind) {
      case "port":
        return `Use ${p.fix.value}`;
      case "tls":
        return `Use ${p.fix.value}`;
      case "pods":
        return "Open pods";
      case "ingress":
        return "Open it";
    }
    return "";
  };

  return (
    <div className="page">
      {dialogs}
      <div className="page-head">
        <div>
          <h1 className="page-title">Services</h1>
          <div className="seg-switch" role="tablist">
            <Link to={`/services${namespace ? "?namespace=" + encodeURIComponent(namespace) : ""}`}>Services</Link>
            <span className="on">Ingress</span>
          </div>
        </div>
      </div>

      <div className="row svc-toolbar">
        {namespaces && namespaces.length > 1 && (
          <select
            className="input"
            style={{ flex: "0 0 auto", width: 260 }}
            value={namespace}
            onChange={(e) => {
              setQuery("");
              setParams({ namespace: e.target.value }, { replace: true });
            }}
            aria-label="Namespace"
          >
            {namespaces.map((ns) => (
              <option key={ns} value={ns}>
                {ns}
              </option>
            ))}
          </select>
        )}
        {namespaces && namespaces.length === 1 && <span className="svc-only-ns">{namespace}</span>}
        <input
          className="input"
          style={{ minWidth: 220 }}
          placeholder="Find a host, path or service…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button
          className={"svc-toggle" + (onlyProblems ? " on" : "")}
          onClick={() => setOnlyProblems((v) => !v)}
          title="Only the addresses with something in the way"
        >
          Problems only <b className={problemCount ? "warn" : ""}>{problemCount}</b>
        </button>
        <span style={{ marginLeft: "auto" }} />
        {data?.mayCreate && (
          <button className="btn" onClick={() => setCreating(true)}>
            + New ingress
          </button>
        )}
      </div>

      {error && <div className="alert">{error}</div>}
      {(!namespaces || loading) && !error && <div className="muted">Loading…</div>}

      {data && !loading && shown.length === 0 && (
        <div className="card">
          <p className="muted" style={{ margin: 0 }}>
            {data.routes.length === 0
              ? `Nothing in ${namespace} is reachable from outside yet. Expose a service from the Services tab.`
              : onlyProblems
                ? "Nothing here needs looking at."
                : "Nothing matches."}
          </p>
        </div>
      )}

      {data && !loading && shown.length > 0 && (
        <div className="card no-pad">
          <table className="table ing-table">
            <thead>
              <tr>
                <th style={{ width: "30%" }}>Host and path</th>
                <th style={{ width: "22%" }}>Goes to</th>
                <th style={{ width: "16%" }}>HTTPS</th>
                <th>State</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => {
                const p = worst(r.problems);
                const key = r.ingress + r.host + r.path;
                return (
                  <tr key={key}>
                    <td>
                      <button className="link-btn mono ing-host" onClick={() => open(r.ingress)} title="Open">
                        {r.host || <span className="muted">any host</span>}
                        <span className="muted">{r.path}</span>
                      </button>
                      {r.host && (
                        <a className="ing-open" href={routeURL(r)} target="_blank" rel="noreferrer" title="Open in a new tab">
                          ↗
                        </a>
                      )}
                      <div className="ing-sub muted">
                        {r.ingress}
                        {r.release && <span className="cm-pill">helm</span>}
                      </div>
                    </td>
                    <td className="mono">
                      {/* Written as the Services tab writes an address. */}
                      {r.service}
                      <span className="svc-ns">.{namespace}</span>:{r.port}
                      {r.ready && (
                        <span className={"muted ing-ready" + (r.ready.ready === 0 ? " warn-text" : "")}>
                          {" "}
                          {r.ready.ready}/{r.ready.total}
                        </span>
                      )}
                    </td>
                    <td className="muted">
                      {r.tlsSecret ? (
                        <>
                          {r.tlsSecret}
                          {r.tlsDays !== undefined && ` · ${r.tlsDays} d`}
                        </>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td>
                      {!p ? (
                        <span className="ing-state ok">works</span>
                      ) : (
                        <div className="ing-problem">
                          <span className={"ing-state " + p.level}>{p.text}</span>
                          {p.fix && (data.mayUpdate || p.fix.kind === "pods" || p.fix.kind === "ingress") && (
                            <button
                              className="btn ghost sm"
                              disabled={busy === key}
                              onClick={() => applyFix(r, p)}
                            >
                              {fixLabel(p)}
                            </button>
                          )}
                          {r.problems.length > 1 && (
                            <span className="muted" title={r.problems.map((x) => x.text).join("\n")}>
                              +{r.problems.length - 1} more
                            </span>
                          )}
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {creating && data && (
        <ExposeDialog
          namespace={namespace}
          services={services.map((s) => s.name)}
          annotationKeys={data.annotationKeys}
          onClose={() => setCreating(false)}
          onCreated={(name, detail) => {
            setCreating(false);
            toast(detail || `${name} created`);
            load();
          }}
        />
      )}

      {editing && data && (
        <IngressEditor
          namespace={namespace}
          name={editing}
          list={data}
          services={services}
          confirm={confirm}
          onClose={() => open("")}
          onSaved={(detail) => {
            toast(detail);
            load();
          }}
          onDeleted={() => {
            open("");
            load();
          }}
        />
      )}
    </div>
  );
}

function toSettings(a: Record<string, string>): Setting[] {
  return Object.keys(a)
    .sort()
    .map((key) => ({ key, value: a[key] }));
}

function formOf(d: IngressDoc, settings: Setting[]): IngressForm {
  return {
    class: d.class,
    rules: d.rules,
    tls: d.tls,
    annotations: Object.fromEntries(settings.filter((s) => s.key.trim()).map((s) => [s.key.trim(), s.value])),
  };
}

function IngressEditor({
  namespace,
  name,
  list,
  services,
  confirm,
  onClose,
  onSaved,
  onDeleted,
}: {
  namespace: string;
  name: string;
  list: IngressList;
  services: { name: string; ports: string[] }[];
  confirm: (o: { title?: string; message: string; okText?: string; danger?: boolean }) => Promise<boolean>;
  onClose: () => void;
  onSaved: (detail: string) => void;
  onDeleted: () => void;
}) {
  const api = useApi();
  const theme = useEditorTheme();
  const [orig, setOrig] = useState<IngressDoc | null>(null);
  const [doc, setDoc] = useState<IngressDoc | null>(null);
  const [settings, setSettings] = useState<Setting[]>([]);
  const [view, setView] = useState<"form" | "yaml">("form");
  const [yamlText, setYamlText] = useState("");
  const [check, setCheck] = useState<IngressRoute[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const timer = useRef<number>();
  const readOnly = !list.mayUpdate;

  const reset = (d: IngressDoc) => {
    setOrig(d);
    setDoc(d);
    setSettings(toSettings(d.annotations));
    setYamlText(d.yaml);
    setCheck(d.routes);
  };

  useEffect(() => {
    api
      .ingress(namespace, name)
      .then(reset)
      .catch((e) => setError(e.message));
  }, [api, namespace, name]);

  const form = useMemo(() => (doc ? formOf(doc, settings) : null), [doc, settings]);
  const dirty =
    !!orig &&
    !!form &&
    (view === "yaml"
      ? yamlText !== orig.yaml
      : JSON.stringify(form) !== JSON.stringify(formOf(orig, toSettings(orig.annotations))));

  useEffect(() => {
    if (!form || view !== "form" || !dirty) return;
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      api
        .checkIngress(namespace, name, form)
        .then((r) => setCheck(r.routes))
        .catch(() => undefined);
    }, 400);
    return () => window.clearTimeout(timer.current);
  }, [api, namespace, name, form, view, dirty]);

  const setRule = (i: number, patch: Partial<IngressDoc["rules"][number]>) =>
    setDoc((d) => d && { ...d, rules: d.rules.map((r, j) => (j === i ? { ...r, ...patch } : r)) });

  const save = async () => {
    if (!doc || !form) return;
    setBusy(true);
    setError("");
    try {
      const r = await api.updateIngress(
        namespace,
        name,
        doc.version,
        view === "yaml" ? { yaml: yamlText } : { form }
      );
      reset(r.ingress);
      onSaved(r.detail);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!doc) return;
    const hosts = doc.routes.filter((r) => r.host).map((r) => r.host + r.path);
    const ok = await confirm({
      title: "Delete " + name,
      message:
        (hosts.length ? `${hosts.join(", ")} will stop answering from outside.` : "") +
        (doc.release ? ` Helm release ${doc.release} puts it back on its next deploy.` : ""),
      okText: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteIngress(namespace, name);
      onDeleted();
    } catch (e: any) {
      setError(e.message);
    }
  };

  const svcPorts = (svc: string) => services.find((s) => s.name === svc)?.ports ?? [];
  const summary = check ? checkSummary(check) : null;
  const certs: IngressCert[] = list.certificates ?? [];

  return (
    <div className="modal-overlay">
      <div className="modal ing-editor" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <div className="muted">{namespace}</div>
            <div className="modal-title">
              {name} {doc?.release && <span className="cm-pill">helm</span>}
            </div>
          </div>
          <div className="row" style={{ gap: 10 }}>
            <div className="seg-switch cm-view" title={dirty ? "Save or discard the changes first" : undefined}>
              {(["form", "yaml"] as const).map((v) => (
                <button key={v} className={view === v ? "on" : ""} disabled={dirty && view !== v} onClick={() => setView(v)}>
                  {v === "form" ? "Form" : "YAML"}
                </button>
              ))}
            </div>
            <button className="icon-btn" onClick={onClose} aria-label="Close">
              ✕
            </button>
          </div>
        </div>
        <div className="modal-body pad">
          {error && <div className="alert">{error}</div>}
          {!doc && !error && <div className="muted">Loading…</div>}
          {doc?.release && (
            <div className="cm-warn">
              ⚠ Installed by Helm release <b>{doc.release}</b>. The next deploy undoes a change here — change the
              chart's values to keep it.
            </div>
          )}

          {doc && view === "yaml" && (
            <div className="ing-yaml">
              <CodeMirror
                value={yamlText}
                theme={theme}
                height="420px"
                extensions={[yaml()]}
                editable={!readOnly}
                onChange={setYamlText}
              />
            </div>
          )}

          {doc && view === "form" && (
            <>
              <div className="field">
                <label className="muted">Class</label>
                <select
                  className="input"
                  value={doc.class}
                  disabled={readOnly}
                  onChange={(e) => setDoc({ ...doc, class: e.target.value })}
                >
                  <option value="">default</option>
                  {Array.from(new Set([...list.classes, doc.class].filter(Boolean))).map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              </div>

              <label className="muted">Paths</label>
              <div className="ing-rules">
                {doc.rules.map((r, i) => (
                  <div className="ing-rule" key={i}>
                    <input
                      className="input sm mono"
                      value={r.host}
                      placeholder="any host"
                      disabled={readOnly}
                      onChange={(e) => setRule(i, { host: e.target.value })}
                    />
                    <input
                      className="input sm mono ing-path"
                      value={r.path}
                      disabled={readOnly}
                      onChange={(e) => setRule(i, { path: e.target.value })}
                    />
                    <span className="muted">→</span>
                    <input
                      className="input sm mono"
                      list="ing-services"
                      value={r.service}
                      disabled={readOnly}
                      onChange={(e) => setRule(i, { service: e.target.value })}
                    />
                    <input
                      className="input sm mono ing-port"
                      list={"ing-ports-" + i}
                      value={r.port}
                      disabled={readOnly}
                      onChange={(e) => setRule(i, { port: e.target.value })}
                    />
                    <datalist id={"ing-ports-" + i}>
                      {svcPorts(r.service).map((p) => (
                        <option key={p} value={p} />
                      ))}
                    </datalist>
                    {!readOnly && (
                      <button
                        className="icon-btn"
                        aria-label="Remove path"
                        onClick={() => setDoc({ ...doc, rules: doc.rules.filter((_, j) => j !== i) })}
                      >
                        ✕
                      </button>
                    )}
                  </div>
                ))}
                <datalist id="ing-services">
                  {services.map((s) => (
                    <option key={s.name} value={s.name} />
                  ))}
                </datalist>
                {!readOnly && (
                  <button
                    className="link-btn"
                    onClick={() => {
                      const last = doc.rules[doc.rules.length - 1];
                      setDoc({
                        ...doc,
                        rules: [
                          ...doc.rules,
                          { host: last?.host ?? "", path: "/", service: last?.service ?? "", port: last?.port ?? "" },
                        ],
                      });
                    }}
                  >
                    + Add path
                  </button>
                )}
                {doc.defaultBackend && (
                  <div className="field-hint">Any other host goes to {doc.defaultBackend} (edit in YAML)</div>
                )}
              </div>

              <label className="muted">HTTPS</label>
              <div className="ing-rules">
                {doc.tls.length === 0 && <div className="field-hint">Plain HTTP: no certificate.</div>}
                {doc.tls.map((t, i) => {
                  const c = certs.find((x) => x.secret === t.secret);
                  return (
                    <div className="ing-tls" key={i}>
                      <select
                        className="input sm"
                        value={t.secret}
                        disabled={readOnly}
                        onChange={(e) =>
                          setDoc({
                            ...doc,
                            tls: doc.tls.map((x, j) => (j === i ? { ...x, secret: e.target.value } : x)),
                          })
                        }
                      >
                        {!c && <option value={t.secret}>{t.secret} — not found</option>}
                        {certs.map((x) => (
                          <option key={x.secret} value={x.secret}>
                            {x.secret} · {x.names.join(", ")} · {x.days} d
                          </option>
                        ))}
                      </select>
                      <span className="muted mono ing-tls-hosts">for {t.hosts.join(", ") || "every host"}</span>
                      {!readOnly && (
                        <button
                          className="icon-btn"
                          aria-label="Remove certificate"
                          onClick={() => setDoc({ ...doc, tls: doc.tls.filter((_, j) => j !== i) })}
                        >
                          ✕
                        </button>
                      )}
                    </div>
                  );
                })}
                {!readOnly && certs.length > 0 && (
                  <button
                    className="link-btn"
                    onClick={() =>
                      setDoc({
                        ...doc,
                        tls: [
                          ...doc.tls,
                          {
                            secret: certs[0].secret,
                            hosts: Array.from(new Set(doc.rules.map((r) => r.host).filter(Boolean))),
                          },
                        ],
                      })
                    }
                  >
                    + Add certificate for these hosts
                  </button>
                )}
              </div>

              <SettingsEditor settings={settings} onChange={setSettings} keys={list.annotationKeys} readOnly={readOnly} />
            </>
          )}

          {summary && view === "form" && (
            <div className={"ing-check " + (summary.ok ? "ok" : "warn")}>
              {summary.ok ? "✓ " : "⚠ "}
              {summary.text}
            </div>
          )}
        </div>
        <div className="modal-actions pad">
          {list.mayDelete && doc && (
            <button className="btn danger" onClick={remove} style={{ marginRight: "auto" }}>
              Delete
            </button>
          )}
          {!readOnly && (
            <>
              <button className="btn ghost" disabled={!dirty || busy} onClick={() => orig && reset(orig)}>
                Discard
              </button>
              <button className="btn" disabled={!dirty || busy} onClick={save}>
                {busy ? "Saving…" : "Save"}
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
