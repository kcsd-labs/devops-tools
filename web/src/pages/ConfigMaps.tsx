import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import CodeMirror from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { indentationMarkers } from "@replit/codemirror-indentation-markers";
import { useEditorTheme } from "../editorTheme";
import { useApi } from "../useApi";
import { useDialogs } from "../components/Dialogs";
import type { ConfigMapData, ConfigMapList, ConfigMapUsage } from "../api";

const VARIABLES = "\u0000variables";
// A tab of one single-value key: the prefix and its place in the list.
const VAR_TAB = "\u0001";
// Up to this many single-value keys get a tab each, as files do; more go in
// one table, where a list of flags reads better than a row of tabs.
const TABS_FOR_VARS = 3;
const VIEW_KEY = "devops-tools.configmapsView";

const FILE_EXTS = new Set([
  ".yml", ".yaml", ".json", ".xml", ".properties", ".conf", ".cfg", ".ini", ".toml",
  ".env", ".sh", ".sql", ".txt", ".js", ".lua", ".tpl", ".pem", ".crt",
]);

/** A key that holds a file rather than one setting: several lines, or a name
    that says it is one. The same test the server uses to describe changes. */
function fileLike(key: string, value: string): boolean {
  const dot = key.lastIndexOf(".");
  return value.includes("\n") || (dot > 0 && FILE_EXTS.has(key.slice(dot).toLowerCase()));
}

interface Var {
  key: string;
  value: string;
}

/** The values split the way they are edited: files in the editor, single
    settings in a table. */
function split(data: Record<string, string>) {
  const files: Record<string, string> = {};
  const vars: Var[] = [];
  for (const k of Object.keys(data).sort()) {
    if (fileLike(k, data[k])) files[k] = data[k];
    else vars.push({ key: k, value: data[k] });
  }
  return { files, vars };
}

function join(files: Record<string, string>, vars: Var[]): Record<string, string> {
  const out: Record<string, string> = { ...files };
  for (const v of vars) if (v.key.trim()) out[v.key.trim()] = v.value;
  return out;
}

function changedKeys(before: Record<string, string>, after: Record<string, string>): string[] {
  const out = new Set<string>();
  for (const k of Object.keys(after)) if (before[k] !== after[k]) out.add(k);
  for (const k of Object.keys(before)) if (!(k in after)) out.add(k);
  return [...out].sort();
}

const KIND_SHORT: Record<string, string> = {
  Deployment: "deploy",
  StatefulSet: "sts",
  DaemonSet: "ds",
  CronJob: "cronjob",
};

/** How one workload reads it, in a line. */
function usageText(u: ConfigMapUsage): string {
  const parts: string[] = [];
  if (u.envAll) parts.push("all keys as env");
  else if (u.env?.length) parts.push(`${u.env.join(", ")} as env`);
  for (const m of u.mounts ?? []) {
    const what = m.keys?.length ? m.keys.join(", ") : "all keys";
    parts.push(`${what} at ${m.path}${m.subPath ? " (subPath)" : ""}`);
  }
  return parts.join(" · ");
}

/** What a saved change needs before a workload sees it. Env values are read
    when a container starts. Mounted files are refreshed in the running pod
    within about a minute — but only if the application reads them again, and
    never under subPath. */
function needs(u: ConfigMapUsage, changed: string[]): "restart" | "refresh" | null {
  const touched = (keys?: string[]) => !keys?.length || keys.some((k) => changed.includes(k));
  const env = (u.envAll && changed.length > 0) || (u.env ?? []).some((k) => changed.includes(k));
  const mounts = (u.mounts ?? []).filter((m) => touched(m.keys));
  if (env || mounts.some((m) => m.subPath)) return "restart";
  if (mounts.length > 0) return "refresh";
  return null;
}

/** Where one key lands in the pods that read it. */
function whereIs(key: string, usedBy: ConfigMapUsage[]): string[] {
  const out: string[] = [];
  for (const u of usedBy) {
    const who = `${KIND_SHORT[u.kind] ?? u.kind} ${u.name}`;
    if (u.envAll || u.env?.includes(key)) out.push(`${who}: env ${key}`);
    for (const m of u.mounts ?? []) {
      if (m.keys?.length && !m.keys.includes(key)) continue;
      out.push(`${who}: ${m.subPath ? m.path : m.path.replace(/\/$/, "") + "/" + key}`);
    }
  }
  return out;
}

/** The tab to open on: the one asked for if it is still there, else the first
    file, else the first single-value key. */
function firstTab(files: Record<string, string>, vars: Var[], keep?: string): string {
  if (keep === VARIABLES && vars.length > TABS_FOR_VARS) return keep;
  if (keep && keep in files) return keep;
  if (keep?.startsWith(VAR_TAB) && vars.length <= TABS_FOR_VARS && Number(keep.slice(1)) < vars.length) return keep;
  const f = Object.keys(files)[0];
  if (f) return f;
  return vars.length > 0 && vars.length <= TABS_FOR_VARS ? VAR_TAB + "0" : VARIABLES;
}

export function ConfigMaps() {
  const api = useApi();
  const theme = useEditorTheme();
  const [params, setParams] = useSearchParams();
  const { dialogs, confirm, alertDlg, toast } = useDialogs();

  const [namespaces, setNamespaces] = useState<string[]>([]);
  const ns = params.get("namespace") ?? "";
  const selected = params.get("name") ?? "";

  const [list, setList] = useState<ConfigMapList | null>(null);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [hideSystem, setHideSystem] = useState(true);

  const [cm, setCm] = useState<ConfigMapData | null>(null);
  const [files, setFiles] = useState<Record<string, string>>({});
  const [vars, setVars] = useState<Var[]>([]);
  const [tab, setTab] = useState<string>(VARIABLES);
  // The whole data: block as kubectl shows it, by default: a ConfigMap is one
  // object, and people who know kubectl read it as one. By key is for editing
  // one large file without the indentation of the block around it.
  const [view, setView] = useState<"yaml" | "keys">(() => {
    try {
      return localStorage.getItem(VIEW_KEY) === "keys" ? "keys" : "yaml";
    } catch {
      return "yaml";
    }
  });
  const [yamlText, setYamlText] = useState("");
  const [adding, setAdding] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState<{ changed: string[]; detail: string } | null>(null);
  const [restarted, setRestarted] = useState<Record<string, boolean>>({});

  const [newOpen, setNewOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const [newYaml, setNewYaml] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);

  const go = useCallback(
    (patch: { namespace?: string; name?: string }) => {
      const next = new URLSearchParams(params);
      for (const [k, v] of Object.entries(patch)) {
        if (v) next.set(k, v);
        else next.delete(k);
      }
      setParams(next, { replace: true });
    },
    [params, setParams]
  );

  useEffect(() => {
    api
      .namespaces()
      .then((r) => {
        setNamespaces(r.namespaces);
        if (!ns && r.namespaces[0]) go({ namespace: r.namespaces[0] });
      })
      .catch((e) => setError(e.message));
    // Only once: the namespace in the address is the selection from then on.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api]);

  const loadList = useCallback(() => {
    if (!ns) return;
    api
      .configMaps(ns)
      .then(setList)
      .catch((e) => {
        setList(null);
        setError(e.message);
      });
  }, [api, ns]);
  useEffect(loadList, [loadList]);

  // keep names the tab to stay on, when it still exists: after a save or a
  // discard the person is still looking at the same key.
  const open = useCallback((data: ConfigMapData, keep?: string) => {
    const s = split(data.data);
    setCm(data);
    setFiles(s.files);
    setVars(s.vars);
    setYamlText(data.yaml);
    setTab(firstTab(s.files, s.vars, keep));
    setAdding(null);
  }, []);

  useEffect(() => {
    try {
      localStorage.setItem(VIEW_KEY, view);
    } catch {
      // A remembered preference, nothing more.
    }
  }, [view]);

  useEffect(() => {
    setSaved(null);
    setRestarted({});
    if (!ns || !selected) {
      setCm(null);
      return;
    }
    api
      .configMap(ns, selected)
      .then(open)
      .catch((e) => {
        setCm(null);
        setError(e.message);
      });
  }, [api, ns, selected, open]);

  const draft = useMemo(() => join(files, vars), [files, vars]);
  const changed = useMemo(() => (cm ? changedKeys(cm.data, draft) : []), [cm, draft]);
  const dirty = view === "yaml" ? !!cm && yamlText !== cm.yaml : changed.length > 0;
  const readOnly = !list?.mayUpdate || !!cm?.immutable;

  const leave = async () =>
    !dirty || (await confirm({ message: "Discard the changes to " + cm?.name + "?", okText: "Discard" }));

  const select = async (name: string) => {
    if (name === selected || !(await leave())) return;
    setError("");
    go({ name });
  };

  const save = async () => {
    if (!cm) return;
    if (view === "keys") {
      const keys = vars.map((v) => v.key.trim()).filter(Boolean);
      const dup = keys.find((k, i) => keys.indexOf(k) !== i || k in files);
      if (dup) {
        await alertDlg(`The key ${dup} appears twice.`);
        return;
      }
    }
    setSaving(true);
    setError("");
    try {
      const before = cm.data;
      const r = await api.updateConfigMap(
        ns,
        cm.name,
        cm.version,
        view === "yaml" ? { yaml: yamlText } : { data: draft }
      );
      open(r.configMap, tab);
      setSaved({ changed: changedKeys(before, r.configMap.data), detail: r.detail });
      setRestarted({});
      loadList();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const restart = async (u: ConfigMapUsage) => {
    try {
      await api.restartWorkload(ns, u.kind, u.name);
      setRestarted((r) => ({ ...r, [u.kind + "/" + u.name]: true }));
      toast(`Restarting ${u.name}`);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const remove = async () => {
    if (!cm) return;
    const users = cm.usedBy.length;
    const ok = await confirm({
      title: "Delete " + cm.name,
      message:
        (users > 0
          ? `${users} ${users === 1 ? "workload reads" : "workloads read"} it — their pods will fail to start until it is back. `
          : "") + "This cannot be undone.",
      okText: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteConfigMap(ns, cm.name);
      go({ name: "" });
      loadList();
      toast("Deleted");
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const exportYaml = async () => {
    if (!cm) return;
    try {
      const text = await api.exportConfigMap(ns, cm.name);
      const a = document.createElement("a");
      a.href = URL.createObjectURL(new Blob([text], { type: "application/yaml" }));
      a.download = `${cm.name}.yaml`;
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const create = async (body: { name: string; yaml?: string } | { manifest: string }) => {
    try {
      const r = await api.createConfigMap(ns, body);
      setNewOpen(false);
      setImportOpen(false);
      setNewName("");
      setNewYaml("");
      setImportText("");
      loadList();
      go({ name: r.name });
      toast("Created " + r.name);
    } catch (e) {
      await alertDlg((e as Error).message);
    }
  };

  const addKey = (raw: string) => {
    const key = raw.trim();
    setAdding(null);
    if (!key) return;
    if (key in draft) {
      alertDlg(`There is already a key ${key}.`);
      return;
    }
    if (fileLike(key, "")) {
      setFiles((f) => ({ ...f, [key]: "" }));
      setTab(key);
    } else {
      const idx = vars.length;
      setVars((v) => [...v, { key, value: "" }]);
      setTab(idx + 1 > TABS_FOR_VARS ? VARIABLES : VAR_TAB + idx);
    }
  };

  const removeFile = async (key: string) => {
    if (!(await confirm({ message: `Remove the key ${key}? It goes when you save.`, okText: "Remove" }))) return;
    setFiles((f) => {
      const next = { ...f };
      delete next[key];
      return next;
    });
    setTab(firstTab(files, vars));
  };

  const visible = (list?.configMaps ?? []).filter(
    (c) =>
      (!hideSystem || !c.system || c.name === selected) &&
      (!search || c.name.toLowerCase().includes(search.toLowerCase()))
  );
  const fileKeys = Object.keys(files).sort();
  const showTable = vars.length > TABS_FOR_VARS || (vars.length === 0 && fileKeys.length === 0);
  // The key on screen, for the heading and the path line.
  const tabKey = tab in files ? tab : tab.startsWith(VAR_TAB) ? (vars[Number(tab.slice(1))]?.key ?? "") : "";
  const affected = saved && cm ? cm.usedBy.map((u) => ({ u, need: needs(u, saved.changed) })).filter((x) => x.need) : [];

  return (
    <div className="page fill-page">
      {dialogs}
      <div className="page-head">
        <div>
          <h1 className="page-title">ConfigMaps</h1>
          <div className="seg-switch" role="tablist">
            <Link to={`/secrets${ns ? "?namespace=" + encodeURIComponent(ns) : ""}`}>Secrets</Link>
            <span className="on">ConfigMaps</span>
          </div>
        </div>
        <div className="row">
          <select
            className="input sm select"
            value={ns}
            onChange={async (e) => {
              // Read before waiting: by the time the question is answered,
              // React has put the controlled select back to the old value.
              const next = e.target.value;
              if (!(await leave())) return;
              go({ namespace: next, name: "" });
            }}
          >
            {!namespaces.includes(ns) && <option value={ns}>{ns || "select a namespace…"}</option>}
            {namespaces.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
          <input
            className="input sm"
            placeholder="Filter ConfigMaps…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <label className="check" title="Hide the cluster's CA bundles and the portal's own">
            <input type="checkbox" checked={hideSystem} onChange={(e) => setHideSystem(e.target.checked)} />
            Hide system
          </label>
          {list?.mayCreate && (
            <>
              <button className="btn ghost" onClick={() => setImportOpen(true)}>
                ⭱ Import
              </button>
              <button className="btn" onClick={() => setNewOpen(true)}>
                + New ConfigMap
              </button>
            </>
          )}
        </div>
      </div>

      {error && (
        <div className="alert" onClick={() => setError("")} title="Click to dismiss">
          {error}
        </div>
      )}

      <div className="cm-layout">
        <div className="card no-pad cm-list">
          {visible.map((c) => (
            <button
              key={c.name}
              className={"cm-item" + (c.name === selected ? " on" : "")}
              onClick={() => select(c.name)}
            >
              <div className="cm-item-name mono">
                {c.name}
                {c.immutable && <span className="cm-pill">immutable</span>}
              </div>
              <div className="cm-item-meta">
                {c.keys} {c.keys === 1 ? "key" : "keys"}
                {c.release && (
                  <>
                    {" · "}
                    <b>helm</b> {c.release}
                  </>
                )}
                {c.usedBy !== undefined &&
                  (c.usedBy === 0 && !c.system ? (
                    <span className="warn-soft"> · used by nothing</span>
                  ) : (
                    ` · used by ${c.usedBy}`
                  ))}
              </div>
            </button>
          ))}
          {list && visible.length === 0 && (
            <div className="muted pad">
              {list.configMaps.length === 0
                ? "No ConfigMaps in this namespace."
                : search
                  ? "Nothing matches."
                  : "Only system ConfigMaps here — untick Hide system to see them."}
            </div>
          )}
        </div>

        {!cm ? (
          <div className="card cm-empty muted">
            {visible.length > 0 ? "Choose a ConfigMap on the left." : " "}
          </div>
        ) : (
          <div className="card no-pad cm-editor">
            <div className="cm-head">
              <div className="row">
                <span className="cm-title mono">
                  {cm.name}
                  {view === "keys" && tabKey && (
                    <>
                      <span className="cm-sep">›</span>
                      <span className="cm-key">{tabKey}</span>
                    </>
                  )}
                </span>
                <span style={{ marginLeft: "auto" }} />
                <div className="seg-switch cm-view" title={dirty ? "Save or discard the changes first" : undefined}>
                  {(["yaml", "keys"] as const).map((v) => (
                    <button
                      key={v}
                      className={view === v ? "on" : ""}
                      disabled={dirty && view !== v}
                      onClick={() => setView(v)}
                    >
                      {v === "yaml" ? "YAML" : "By key"}
                    </button>
                  ))}
                </div>
                <button className="btn ghost sm" onClick={exportYaml} title="Download as a manifest">
                  Export
                </button>
                {list?.mayDelete && (
                  <button className="btn danger sm" onClick={remove}>
                    Delete
                  </button>
                )}
              </div>
              <div className="cm-used">
                <span className="muted">Used by</span>{" "}
                {cm.usageUnknown
                  ? "— the workloads could not be read"
                  : cm.usedBy.length === 0
                    ? "nothing in this namespace"
                    : cm.usedBy.map((u, i) => (
                        <span key={u.kind + u.name}>
                          {i > 0 && "; "}
                          <b>
                            {KIND_SHORT[u.kind] ?? u.kind} {u.name}
                          </b>{" "}
                          <span className="muted">— {usageText(u)}</span>
                        </span>
                      ))}
              </div>
              {cm.release && (
                <div className="cm-warn">
                  ⚠ Installed by Helm release <b>{cm.release}</b>. A change here is undone by its next deploy —
                  change the chart's values to keep it.
                </div>
              )}
              {cm.immutable && (
                <div className="cm-warn">
                  Immutable: Kubernetes refuses any change to it. To change it, create a new one and point the
                  workloads at it.
                </div>
              )}
            </div>

            {view === "yaml" ? (
              <div className="cm-body">
                <CodeMirror
                  value={yamlText}
                  theme={theme}
                  height="100%"
                  extensions={[yaml(), indentationMarkers()]}
                  editable={!readOnly}
                  onChange={setYamlText}
                />
              </div>
            ) : (
              <>
            <div className="cm-keys-caption">
              Keys in this ConfigMap · {fileKeys.length + vars.length + (cm.binaryKeys?.length ?? 0)}
            </div>
            <div className="tabs cm-tabs">
              {fileKeys.map((k) => (
                <button key={k} className={"tab mono" + (tab === k ? " active" : "")} onClick={() => setTab(k)}>
                  {k}
                  {cm.data[k] !== files[k] && <span className="cm-dot"> ●</span>}
                  {!readOnly && tab === k && (
                    <span
                      className="cm-x"
                      title="Remove this key"
                      onClick={(e) => {
                        e.stopPropagation();
                        removeFile(k);
                      }}
                    >
                      ×
                    </span>
                  )}
                </button>
              ))}
              {showTable ? (
                <button
                  className={"tab" + (tab === VARIABLES ? " active" : "")}
                  onClick={() => setTab(VARIABLES)}
                >
                  Variables <span className="muted">· {vars.length}</span>
                </button>
              ) : (
                vars.map((v, i) => (
                  <button
                    key={"v" + i}
                    className={"tab mono" + (tab === VAR_TAB + i ? " active" : "")}
                    onClick={() => setTab(VAR_TAB + i)}
                  >
                    {v.key || "(new key)"}
                    <span className="cm-inline-value"> = {JSON.stringify(v.value)}</span>
                    {(cm.data[v.key] !== v.value || !(v.key in cm.data)) && <span className="cm-dot"> ●</span>}
                  </button>
                ))
              )}
              {(cm.binaryKeys ?? []).map((k) => (
                <span key={k} className="tab mono cm-binary" title="Binary data is shown, not edited here">
                  {k} (binary)
                </span>
              ))}
              {!readOnly &&
                (adding === null ? (
                  <button className="link-btn cm-add" onClick={() => setAdding("")}>
                    + Add key
                  </button>
                ) : (
                  <input
                    className="input sm cm-add-input mono"
                    autoFocus
                    placeholder="application.yml or LOG_LEVEL"
                    value={adding}
                    onChange={(e) => setAdding(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") addKey(adding);
                      if (e.key === "Escape") setAdding(null);
                    }}
                    onBlur={() => addKey(adding)}
                  />
                ))}
            </div>

            {tabKey && !cm.usageUnknown && (
              <div className="cm-where mono">
                {whereIs(tabKey, cm.usedBy).length > 0
                  ? "→ in the pod: " + whereIs(tabKey, cm.usedBy).join(" · ")
                  : "→ no workload here reads this key"}
              </div>
            )}

            <div className="cm-body">
              {tab !== VARIABLES && tab in files ? (
                <CodeMirror
                  value={files[tab]}
                  theme={theme}
                  height="100%"
                  extensions={/\.ya?ml$/i.test(tab) ? [yaml(), indentationMarkers()] : [indentationMarkers()]}
                  editable={!readOnly}
                  onChange={(v) => setFiles((f) => ({ ...f, [tab]: v }))}
                />
              ) : (
                <div className="cm-vars">
                  {vars.length === 0 && (
                    <div className="muted">Empty. Add a key, or write the whole ConfigMap in the YAML view.</div>
                  )}
                  {vars.length > 0 && (
                    <div className="cm-var-head">
                      <span>Key</span>
                      <span>Value</span>
                    </div>
                  )}
                  {vars.map((v, i) => {
                    if (!showTable && tab !== VAR_TAB + i) return null;
                    const changedHere = cm.data[v.key] !== v.value || !(v.key in cm.data);
                    return (
                      <div className="cm-var" key={i}>
                        <input
                          className="input sm mono"
                          value={v.key}
                          readOnly={readOnly}
                          onChange={(e) =>
                            setVars((vs) => vs.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))
                          }
                        />
                        <input
                          className={"input sm mono" + (changedHere ? " cm-changed" : "")}
                          value={v.value}
                          readOnly={readOnly}
                          onChange={(e) =>
                            setVars((vs) => vs.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))
                          }
                        />
                        {!readOnly && (
                          <button
                            className="cm-x"
                            title="Remove"
                            onClick={() => {
                              const rest = vars.filter((_, j) => j !== i);
                              setVars(rest);
                              setTab(firstTab(files, rest));
                            }}
                          >
                            ×
                          </button>
                        )}
                      </div>
                    );
                  })}
                  {!readOnly && showTable && (
                    <button className="link-btn" onClick={() => setVars((vs) => [...vs, { key: "", value: "" }])}>
                      + Add variable
                    </button>
                  )}
                </div>
              )}
            </div>
              </>
            )}

            {saved && (
              <div className="cm-saved">
                <div>
                  ✓ Saved{saved.detail ? <span className="muted"> — {saved.detail}</span> : null}
                </div>
                {affected.length === 0 && cm.usedBy.length > 0 && (
                  <div className="muted">Nothing that reads it uses the keys that changed.</div>
                )}
                {affected.map(({ u, need }) => {
                  const id = u.kind + "/" + u.name;
                  const canRestart = list?.mayRestart && u.kind !== "CronJob";
                  return (
                    <div className="cm-apply" key={id}>
                      <b>{u.name}</b>{" "}
                      <span className="muted">
                        {u.kind === "CronJob"
                          ? "— its next run reads the new values."
                          : need === "restart"
                            ? "— reads these keys at start: it needs a restart to see them."
                            : "— mounted files refresh within a minute, if the app reads them again."}
                      </span>
                      {canRestart &&
                        (restarted[id] ? (
                          <span className="cm-done">restarting…</span>
                        ) : (
                          <button
                            className={"btn sm " + (need === "restart" ? "" : "ghost")}
                            onClick={() => restart(u)}
                          >
                            Restart {u.name}
                          </button>
                        ))}
                    </div>
                  );
                })}
              </div>
            )}

            {!readOnly && (
              <div className="cm-foot">
                <span className={dirty ? "warn-soft" : "muted"}>
                  {!dirty
                    ? "No changes"
                    : view === "yaml"
                      ? "Edited — what changed is listed after saving"
                      : `Changed: ${changed.join(", ")}`}
                </span>
                <span style={{ marginLeft: "auto" }} />
                <button className="btn ghost" disabled={!dirty} onClick={() => open(cm, tab)}>
                  Discard
                </button>
                <button className="btn" disabled={!dirty || saving} onClick={save}>
                  {saving ? "Saving…" : "Save"}
                </button>
              </div>
            )}
          </div>
        )}
      </div>

      {newOpen && (
        <div className="modal-overlay" onClick={() => setNewOpen(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div className="modal-title">New ConfigMap in {ns}</div>
            </div>
            <div className="modal-body pad">
              <input
                className="input mono"
                autoFocus
                placeholder="name, e.g. app-config"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
              />
              <div className="muted" style={{ margin: "12px 0 6px", fontSize: 12.5 }}>
                Its keys, as in a manifest — or leave this empty and add them afterwards.
              </div>
              <textarea
                className="input mono cm-import"
                spellCheck={false}
                value={newYaml}
                onChange={(e) => setNewYaml(e.target.value)}
                placeholder={"data:\n  LOG_LEVEL: INFO\n  application.yml: |\n    server:\n      port: 8080"}
              />
            </div>
            <div className="modal-actions">
              <button className="btn ghost" onClick={() => setNewOpen(false)}>
                Cancel
              </button>
              <button
                className="btn"
                disabled={!newName.trim()}
                onClick={() => create({ name: newName.trim(), yaml: newYaml.trim() ? newYaml : undefined })}
              >
                Create
              </button>
            </div>
          </div>
        </div>
      )}

      {importOpen && (
        <div className="modal-overlay" onClick={() => setImportOpen(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div className="modal-title">Import a ConfigMap into {ns}</div>
            </div>
            <div className="modal-body pad">
              <div className="muted" style={{ fontSize: 12.5, marginBottom: 8 }}>
                Paste a manifest — one exported from another environment, or any ConfigMap in YAML or JSON — or{" "}
                <button className="link-btn" onClick={() => fileInput.current?.click()}>
                  choose a file
                </button>
                .
              </div>
              <input
                ref={fileInput}
                type="file"
                accept=".yaml,.yml,.json"
                hidden
                onChange={async (e) => {
                  const f = e.target.files?.[0];
                  e.target.value = "";
                  if (f) setImportText(await f.text());
                }}
              />
              <textarea
                className="input mono cm-import"
                spellCheck={false}
                value={importText}
                onChange={(e) => setImportText(e.target.value)}
                placeholder={"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app-config\ndata:\n  LOG_LEVEL: INFO"}
              />
            </div>
            <div className="modal-actions">
              <button className="btn ghost" onClick={() => setImportOpen(false)}>
                Cancel
              </button>
              <button className="btn" disabled={!importText.trim()} onClick={() => create({ manifest: importText })}>
                Import
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
