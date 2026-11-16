import { useEffect, useMemo, useRef, useState } from "react";
import CodeMirror, {
  Decoration,
  EditorView,
  RangeSetBuilder,
  StateEffect,
  StateField,
  type DecorationSet,
} from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { useApi } from "../useApi";
import { useEditorTheme } from "../editorTheme";
import {
  CatalogError,
  type CatalogAsk,
  type CatalogChart,
  type CatalogCheck,
  type ClusterHints,
  type HelmRequest,
  type HelmTemplate,
  type ValuesFill,
} from "../api";
import { Loading, ProblemList, ago, objectSummary } from "./Catalog";
import { Markdown } from "./Markdown";
import { keyCompletion, keyDocs } from "./yamlKeys";

// The namespace this browser last installed into, offered first next time,
// and which view of the dialog it last used.
const NAMESPACE_KEY = "devops-tools.helmPackages.namespace";
const VIEW_KEY = "devops-tools.helmPackages.view";
const NEW = "\u0000new";

function remembered(key: string): string {
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

function remember(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* a convenience only */
  }
}

/** value, once it has stopped changing for ms. */
function useSettled<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

const namespaceRE = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;

type InstallProps = {
  /** Fixed for an upgrade; chosen in the dialog for an install. */
  namespace?: string;
  repo: string;
  chart: string;
  kind: "install" | "upgrade";
  /** Fixed for an upgrade; suggested for an install. */
  release?: string;
  fromVersion?: string;
  initialValues?: string;
  initialVersion?: string;
  /** An administrator: installs straight away, no request. */
  direct: boolean;
  /** May ask for the release to go into a namespace that is not there yet. */
  newNamespace?: boolean;
  onClose: () => void;
  onDone: (r: HelmRequest) => void;
};

type View = "simple" | "values";

/** The dialog a chart is installed or upgraded from. Questions on the left;
    on the right either what the release will give and the values it writes
    (Simple), or the values themselves, the chart's defaults and README. */
export function ChartInstall(props: InstallProps) {
  const { repo, chart, kind, direct } = props;
  const fixedNamespace = !!props.namespace;
  const api = useApi();
  const theme = useEditorTheme();
  const [namespace, setNamespace] = useState(props.namespace ?? "");
  const [namespaces, setNamespaces] = useState<string[] | null>(fixedNamespace ? [props.namespace!] : null);
  // A namespace to be created: what is typed, and what the dialog works with.
  const [isNew, setIsNew] = useState(false);
  const [newName, setNewName] = useState("");
  const settledNew = useSettled(newName, 500);
  const target = isNew ? settledNew : namespace;
  const [nameTouched, setNameTouched] = useState(!!props.release);
  const [info, setInfo] = useState<CatalogChart | null>(null);
  const [loadError, setLoadError] = useState("");
  const [version, setVersion] = useState(props.initialVersion ?? "");
  const [release, setRelease] = useState(props.release ?? "");
  const [values, setValues] = useState(props.initialValues ?? "");
  const [view, setView] = useState<View>(() => {
    const v = remembered(VIEW_KEY);
    if (kind === "upgrade") return "values";
    return v === "simple" || v === "values" ? v : direct ? "values" : "simple";
  });
  const [tab, setTab] = useState<"values" | "defaults" | "readme" | "after">("values");
  const [check, setCheck] = useState<CatalogCheck>();
  const [checkedKey, setCheckedKey] = useState("");
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [busy, setBusy] = useState(false);
  const [submitError, setSubmitError] = useState("");
  // The editor alone on the whole window.
  const [expanded, setExpanded] = useState(false);
  // Starting values from the administrators: applied once, undone on request.
  const [template, setTemplate] = useState<HelmTemplate | undefined>();
  const [templateUsed, setTemplateUsed] = useState(false);
  const templateTried = useRef(false);
  // A new form when the values are started over.
  const [formKey, setFormKey] = useState(0);
  // Every key the chart asked for, kept once asked so the field stays.
  const [asked, setAsked] = useState<{ key: string; said: string }[]>([]);
  const askedRef = useRef(asked);
  askedRef.current = asked;

  const chooseView = (v: View) => {
    setView(v);
    remember(VIEW_KEY, v);
    if (v === "simple") setExpanded(false);
  };

  // Esc goes back from the whole window — unless it closes CodeMirror's
  // search panel.
  useEffect(() => {
    if (!expanded) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || (e.target as HTMLElement | null)?.closest?.(".cm-editor")) return;
      setExpanded(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [expanded]);

  // Where it may go: the namespaces the caller may install into, the one
  // used last offered first.
  useEffect(() => {
    if (fixedNamespace) return;
    api
      .catalogNamespaces()
      .then((r) => {
        setNamespaces(r.namespaces);
        const last = remembered(NAMESPACE_KEY);
        setNamespace((cur) => cur || (r.namespaces.includes(last) ? last : (r.namespaces[0] ?? "")));
        if (r.namespaces.length === 0 && props.newNamespace) setIsNew(true);
      })
      .catch((e) => setLoadError(e.message));
  }, [api, fixedNamespace, props.newNamespace]);

  // The chart, for the chosen version; and a release name free in the
  // chosen namespace, unless one was typed.
  const newOK = !isNew || namespaceRE.test(settledNew);
  useEffect(() => {
    let live = true;
    setLoadError("");
    api
      .catalogChart(repo, chart, version, newOK ? target : "", isNew && newOK)
      .then((c) => {
        if (!live) return;
        setInfo(c);
        if (!version) setVersion(c.version);
        if (kind === "install" && !nameTouched && c.suggestedName) setRelease(c.suggestedName);
        setTemplate(c.template);
      })
      .catch((e) => live && setLoadError(e.message));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, target, isNew, repo, chart, version]);

  // The administrators' starting values, once, for a fresh install.
  useEffect(() => {
    if (templateTried.current || !info || kind !== "install") return;
    templateTried.current = true;
    if (info.template && !props.initialValues) {
      setValues(info.template.values);
      setTemplateUsed(true);
    }
  }, [info, kind, props.initialValues]);

  const namespaceProblem = isNew
    ? !newName
      ? "Type a name for the new namespace."
      : !namespaceRE.test(newName)
        ? "Lower-case letters, digits and dashes, at most 63 long."
        : newName === settledNew
          ? (info?.namespaceProblem ?? "")
          : ""
    : "";

  const ask: CatalogAsk | null = useMemo(
    () =>
      info && version && release && target && !namespaceProblem && (!isNew || newName === settledNew)
        ? { kind, release, repo, chart, version, values, newNamespace: isNew }
        : null,
    [info, version, release, target, namespaceProblem, isNew, newName, settledNew, kind, repo, chart, values]
  );
  const key = ask ? JSON.stringify({ ...ask, target }) : "";

  // Checked again shortly after each change: what is installed is exactly
  // what was last checked.
  // A newer check abandons the one still running: the server stops working on
  // values nobody looks at any more.
  const seq = useRef(0);
  const running = useRef<AbortController | null>(null);
  const [checkStarted, setCheckStarted] = useState(0);
  useEffect(() => {
    if (!ask) return;
    const n = ++seq.current;
    setChecking(true);
    const t = setTimeout(() => {
      running.current?.abort();
      const ctl = new AbortController();
      running.current = ctl;
      setCheckStarted(Date.now());
      api
        .catalogCheck(target, { ...ask, known: askedRef.current.map((a) => a.key) }, ctl.signal)
        .then((c) => {
          if (n !== seq.current) return;
          setCheck(c);
          setCheckedKey(key);
          setCheckError("");
          if (c.required?.length) {
            setAsked((cur) => [...cur, ...c.required!.filter((r) => !cur.some((x) => x.key === r.key))]);
          }
        })
        .catch((e) => {
          if (e?.name === "AbortError") return;
          if (n === seq.current) {
            setCheckError(e.message);
            setCheck(undefined);
          }
        })
        .finally(() => n === seq.current && setChecking(false));
    }, 600);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  useEffect(() => () => running.current?.abort(), []);

  const ready = !!check?.ok && checkedKey === key && !checking && !busy;

  const submit = async () => {
    if (!ask) return;
    setBusy(true);
    setSubmitError("");
    try {
      const q = await api.catalogRequest(target, ask);
      if (!isNew) remember(NAMESPACE_KEY, target);
      props.onDone(q);
    } catch (e: any) {
      if (e instanceof CatalogError && e.check) setCheck(e.check);
      setSubmitError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const verb = kind === "upgrade" ? "Upgrade" : "Install";

  // What the questions on the left wrote, lit up in the editor for a moment.
  const viewRef = useRef<EditorView | null>(null);
  const [flash, setFlash] = useState<number[]>();
  const valuesRef = useRef(values);
  valuesRef.current = values;
  const filled = (text: string) => {
    setFlash(changedLines(valuesRef.current, text));
    setValues(text);
  };
  useEffect(() => {
    const v = viewRef.current;
    if (!flash || !v || !v.dom.isConnected) return;
    const first = flash.find((n) => n <= v.state.doc.lines);
    v.dispatch({
      effects: [
        flashLines.of(flash),
        ...(first ? [EditorView.scrollIntoView(v.state.doc.line(first).from, { y: "center" })] : []),
      ],
    });
    const t = setTimeout(() => viewRef.current?.dom.isConnected && viewRef.current.dispatch({ effects: flashLines.of([]) }), 1800);
    return () => clearTimeout(t);
  }, [flash]);

  // A check that cannot pass opens its reasons; warnings wait for a click.
  const [showProblems, setShowProblems] = useState(false);
  useEffect(() => {
    if (check && !check.ok) setShowProblems(true);
  }, [check]);

  const startOver = () => {
    setValues("");
    setTemplateUsed(false);
    setFormKey((k) => k + 1);
  };

  const docs = info?.docs ?? {};
  const editorHelp = useMemo(() => [keyDocs(docs), keyCompletion(docs)], [docs]);
  const stillAsked = new Set((check?.required ?? []).map((r) => r.key));

  return (
    <div className="modal-overlay">
      <div className={"modal cat-install" + (expanded ? " expanded" : "")} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="cat-head">
            {info?.logo && <img className="cat-logo" src={info.logo} alt="" />}
            <div className="cat-head-text">
              <div className="modal-title">
                {chart} <span className="cm-pill">{info?.repoTitle ?? repo}</span>
                {kind === "upgrade" && (
                  <span className="muted cat-head-what">
                    {" "}
                    · upgrade {props.release} in {namespace}
                  </span>
                )}
              </div>
              {info?.description && (
                <div className="muted cat-desc-line" title={info.repoUrl}>
                  {info.description}
                </div>
              )}
            </div>
          </div>
          <div className="row cat-head-actions">
            {info && (
              <div className="seg-switch cat-view" role="tablist" aria-label="How to fill it in">
                <button className={view === "simple" ? "on" : ""} onClick={() => chooseView("simple")}>
                  Simple
                </button>
                <button className={view === "values" ? "on" : ""} onClick={() => chooseView("values")}>
                  Values (YAML)
                </button>
              </div>
            )}
            <button className="icon-btn" onClick={props.onClose} aria-label="Close">
              ✕
            </button>
          </div>
        </div>

        {(!info || namespaces?.length === 0) && !(namespaces?.length === 0 && props.newNamespace) && (
          <div className="modal-body pad">
            {loadError && <div className="alert">{loadError}</div>}
            {!info && !loadError && (
              <Loading
                text="Fetching the chart…"
                bar
                slow="The chart is downloaded from where its author publishes it — a large one can take up to a minute."
              />
            )}
            {namespaces?.length === 0 && !props.newNamespace && (
              <div className="alert">There is no namespace you may install into. Ask for helm-install on one.</div>
            )}
          </div>
        )}

        {info && (
          <div className="cat-body">
            {/* Hidden, not unmounted, on the whole window: the form keeps what it wrote. */}
            <aside className="cat-side" hidden={expanded}>
              {loadError && <div className="alert">{loadError}</div>}
              <div className="cat-side-title">Where</div>
              <div className="field">
                <label className="muted">Release name</label>
                <input
                  className="input mono"
                  value={release}
                  disabled={kind === "upgrade"}
                  onChange={(e) => {
                    setNameTouched(true);
                    setRelease(e.target.value.toLowerCase());
                  }}
                />
              </div>
              <div className="field">
                <label className="muted">Namespace</label>
                {fixedNamespace ? (
                  <input className="input mono" value={namespace} disabled />
                ) : (
                  <select
                    className="input mono"
                    value={isNew ? NEW : namespace}
                    onChange={(e) => {
                      if (e.target.value === NEW) {
                        setIsNew(true);
                      } else {
                        setIsNew(false);
                        setNamespace(e.target.value);
                      }
                    }}
                  >
                    {(namespaces ?? []).map((ns) => (
                      <option key={ns} value={ns}>
                        {ns}
                      </option>
                    ))}
                    {props.newNamespace && <option value={NEW}>+ New namespace…</option>}
                  </select>
                )}
                {isNew && (
                  <div className="cat-new-ns">
                    <label className="muted">Name of the new namespace</label>
                    <input
                      className="input mono"
                      value={newName}
                      autoFocus
                      placeholder="team-app-dev"
                      onChange={(e) => setNewName(e.target.value.toLowerCase().trim())}
                      aria-label="Name of the new namespace"
                    />
                    {namespaceProblem ? (
                      <div className="field-hint cat-kept">{namespaceProblem}</div>
                    ) : (
                      <div className="field-hint">
                        {direct
                          ? "Created together with the release."
                          : "Created when an administrator approves — they can give your role access to it at the same time."}
                      </div>
                    )}
                  </div>
                )}
              </div>
              <div className="field">
                <label className="muted">Version{props.fromVersion ? ` (now ${props.fromVersion})` : ""}</label>
                <select className="input" value={version} onChange={(e) => setVersion(e.target.value)}>
                  {info.versions.slice(0, 60).map((v, i) => (
                    <option key={v.version} value={v.version}>
                      {v.version}
                      {v.appVersion ? ` · app ${v.appVersion}` : ""}
                      {i === 0 ? " (latest)" : ""}
                      {v.prerelease ? " · pre-release" : ""}
                    </option>
                  ))}
                </select>
              </div>

              {templateUsed && template && (
                <div className="cat-template">
                  <div>
                    <b>Starting values from your administrators</b>
                    {template.note && <> — {template.note}</>}
                  </div>
                  <div className="muted">
                    by {template.author}, {ago(template.updated)} ·{" "}
                    <button className="link-btn" onClick={startOver}>
                      start from the chart's own instead
                    </button>
                  </div>
                </div>
              )}

              <InstallForm
                key={formKey}
                info={info}
                hints={info.cluster}
                kind={kind}
                release={release}
                values={values}
                current={() => valuesRef.current}
                onFilled={filled}
                asked={asked}
                stillAsked={stillAsked}
                refusal={check && !check.ok ? check.refusal : undefined}
              />

              {direct && kind === "install" && (
                <TemplateAdmin
                  repo={info.repo}
                  chart={info.name}
                  values={values}
                  template={template}
                  onChanged={setTemplate}
                />
              )}
            </aside>

            <section className="cat-main">
              {view === "simple" ? (
                <SimpleSide
                  values={values}
                  theme={theme}
                  help={editorHelp}
                  onEditor={(v) => (viewRef.current = v)}
                  onEdit={() => {
                    chooseView("values");
                    setTab("values");
                  }}
                />
              ) : (
                <>
                  <div className="cat-tabbar">
                    <div className="seg-switch cat-tabs">
                      <button className={tab === "values" ? "on" : ""} onClick={() => setTab("values")}>
                        Your values
                      </button>
                      <button className={tab === "defaults" ? "on" : ""} onClick={() => setTab("defaults")}>
                        Chart defaults
                      </button>
                      <button className={tab === "readme" ? "on" : ""} onClick={() => setTab("readme")}>
                        README
                      </button>
                      <button className={tab === "after" ? "on" : ""} onClick={() => setTab("after")}>
                        After install
                      </button>
                    </div>
                    {expanded && (
                      <span className="muted mono cat-where">
                        {release} in {target} · {version}
                      </span>
                    )}
                    <button
                      className="btn ghost sm cat-expand"
                      onClick={() => setExpanded((x) => !x)}
                      title={expanded ? "Show the questions again (Esc)" : "Hide the questions, give the editor the whole window"}
                    >
                      {expanded ? "↙ Show the questions" : "↗ Full screen"}
                    </button>
                  </div>
                  <div className="field-hint cat-main-hint">
                    {tab === "values" &&
                      (kind === "upgrade"
                        ? "What this release was given last time, on top of the chart's defaults. Change what you need."
                        : "Only what differs from the chart's defaults — the questions on the left write here too. Hover a key for what the chart says about it; Ctrl+Space lists the keys.")}
                    {tab === "defaults" && "Everything the chart can be given, with its defaults. Read only; Ctrl+F searches, hover a key for its meaning."}
                    {tab === "readme" && info.repoUrl}
                    {tab === "after" && "What the chart says comes after the install, with these values."}
                  </div>
                  {tab === "values" && (
                    <div className="cat-grow">
                      <CodeMirror
                        value={values}
                        theme={theme}
                        height="100%"
                        extensions={[yaml(), flashField, ...editorHelp]}
                        placeholder={"# e.g.\n# replicaCount: 2\n# resources:\n#   limits: { memory: 512Mi }"}
                        onChange={setValues}
                        onCreateEditor={(v) => (viewRef.current = v)}
                      />
                    </div>
                  )}
                  {tab === "defaults" && (
                    <div className="cat-grow">
                      <CodeMirror
                        value={info.defaults}
                        theme={theme}
                        height="100%"
                        extensions={[yaml(), EditorView.lineWrapping, keyDocs(docs)]}
                        editable={false}
                      />
                    </div>
                  )}
                  {tab === "readme" && (
                    <div className="cat-readme cat-readme-md cat-grow">
                      {info.readme ? <Markdown source={info.readme} /> : "The chart has no README."}
                    </div>
                  )}
                  {tab === "after" && (
                    <div className="cat-readme cat-grow cat-notes">
                      <Notes check={check} checking={checking} />
                    </div>
                  )}
                </>
              )}
            </section>
          </div>
        )}

        {showProblems && check && check.problems.length > 0 && (
          <div className="cat-problems">
            <ProblemList problems={check.problems} />
          </div>
        )}
        {submitError && <div className="alert cat-submit-error">{submitError}</div>}

        <div className="modal-actions cat-foot">
          <CheckLine
            check={check}
            checking={checking}
            since={checkStarted}
            error={checkError}
            open={showProblems}
            onToggle={() => setShowProblems((x) => !x)}
          />
          <span className="muted cat-note">
            {direct
              ? "You're an administrator: runs straight away, written to the audit trail"
              : "🔒 An administrator approves it first"}
          </span>
          <button className="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button className="btn" disabled={!ready} onClick={submit}>
            {busy ? "Sending…" : direct ? verb : "Need approve"}
          </button>
        </div>
      </div>
    </div>
  );
}

/** Simple view: what the answers wrote into the values. The chart's own notes
    are in Values (YAML), under After install. */
function SimpleSide({
  values,
  theme,
  help,
  onEditor,
  onEdit,
}: {
  values: string;
  theme: ReturnType<typeof useEditorTheme>;
  help: ReturnType<typeof keyDocs>[];
  onEditor: (v: EditorView) => void;
  onEdit: () => void;
}) {
  return (
    <div className="cat-simple">
      <div className="cat-simple-block cat-simple-values">
        <div className="cat-simple-head">
          <div className="cat-side-title">What this writes into the values</div>
          <button className="btn ghost sm" onClick={onEdit}>
            Edit the values
          </button>
        </div>
        {values.trim() ? (
          <div className="cat-grow">
            <CodeMirror
              value={values}
              theme={theme}
              height="100%"
              editable={false}
              extensions={[yaml(), flashField, EditorView.lineWrapping, ...help]}
              onCreateEditor={onEditor}
            />
          </div>
        ) : (
          <div className="muted cat-empty-values">
            Nothing yet — the chart installs as its author ships it. Answer the questions on the left to change that.
          </div>
        )}
      </div>
    </div>
  );
}

/** The chart's NOTES.txt as it would print after the install. */
function Notes({ check, checking }: { check?: CatalogCheck; checking: boolean }) {
  if (!check) return <div className="muted">{checking ? "Checking the chart…" : "Shown once the chart is checked."}</div>;
  if (!check.ok) return <div className="muted">Shown once the check passes.</div>;
  if (!check.notes) {
    return (
      <div className="muted">
        The chart says nothing about what comes after. Once it runs, its addresses are on the Services page, and its
        pods on the Pods page.
      </div>
    );
  }
  return <pre className="cat-notes-text">{check.notes}</pre>;
}

/** The questions: what the chart asks for, and what the portal can fill in. */
function InstallForm({
  info,
  hints,
  kind,
  release,
  values,
  current,
  onFilled,
  asked,
  stillAsked,
  refusal,
}: {
  info: CatalogChart;
  hints?: ClusterHints;
  kind: "install" | "upgrade";
  release: string;
  values: string;
  /** The values now — they may change while a request is out. */
  current: () => string;
  onFilled: (v: string) => void;
  asked: { key: string; said: string }[];
  stillAsked: Set<string>;
  refusal?: { where?: string; messages: string[] };
}) {
  const api = useApi();
  const settings = info.settings;
  const docs = info.docs;
  const install = kind === "install";
  const defaultClass = hints?.storageClasses.find((c) => c.default)?.name ?? "";
  const [typed, setTyped] = useState<Record<string, string>>({});
  const [extra, setExtra] = useState<Record<string, string>>({});
  const [storage, setStorage] = useState("");
  const [disk, setDisk] = useState("");
  const [preset, setPreset] = useState("");
  const [replicas, setReplicas] = useState<Record<string, string>>({});
  const [expose, setExpose] = useState(false);
  const [spot, setSpot] = useState(0);
  const [host, setHost] = useState("");
  const [hostTouched, setHostTouched] = useState(false);
  const [ingressClass, setIngressClass] = useState(hints?.ingressClass ?? "");
  const [tls, setTls] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [kept, setKept] = useState<string[]>([]);

  const sp = settings.ingresses[spot];
  const domainShape = sp?.shape === "domain";
  // The host follows the release name until someone types one.
  const suggested = hints?.domain ? (domainShape ? hints.domain : release ? `${release}.${hints.domain}` : "") : "";
  useEffect(() => {
    if (!hostTouched) setHost(suggested);
  }, [suggested, hostTouched]);
  useEffect(() => {
    if (hints?.ingressClass && !ingressClass) setIngressClass(hints.ingressClass);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hints?.ingressClass]);
  // The certificate follows the host until someone picks one.
  const probe = domainShape ? `x.${host}` : host;
  const certs = hints?.certs ?? [];
  const matching = certs.find((c) => covers(c.names, probe))?.name ?? "";
  const cert = tls ?? matching;

  const replicaCounts: Record<string, number> = {};
  for (const [p, v] of Object.entries(replicas)) {
    if (/^\d+$/.test(v.trim())) replicaCounts[p] = Number(v.trim());
  }
  const set: Record<string, string> = {};
  for (const [k, v] of Object.entries(typed)) if (v.trim()) set[k] = v;
  const more: Record<string, string> = {};
  for (const [k, v] of Object.entries(extra)) if (v.trim() && !set[k]) more[k] = v;
  const diskOK = /^[0-9]+(\.[0-9]+)?(Mi|Gi|Ti)$/.test(disk);
  const fill: ValuesFill = install
    ? {
        storageClass: storage,
        storagePaths: settings.storage,
        disk: diskOK ? disk : "",
        diskPaths: settings.disks,
        preset,
        presetPaths: settings.presets,
        replicas: replicaCounts,
        set,
        extra: more,
        ingress: expose && sp && host ? { ...sp, host, class: ingressClass, tls: cert } : undefined,
      }
    : { set, extra: more };
  const fillKey = JSON.stringify(fill);

  // What was last written, so the next change can take it out again.
  const previous = useRef<ValuesFill>({});
  const written = useRef<string>("");
  useEffect(() => {
    if (written.current === "") {
      // The form starts empty: nothing to write until something is chosen.
      written.current = fillKey;
      return;
    }
    if (fillKey === written.current) return;
    let live = true;
    const run = async () => {
      const sent = current();
      try {
        const res = await api.fillValues(sent, fill, previous.current);
        if (!live) return;
        if (current() !== sent) return run(); // typed meanwhile: again, on what is there now
        previous.current = fill;
        written.current = fillKey;
        setKept(res.kept);
        setError("");
        if (res.values !== sent) onFilled(res.values);
      } catch (e: any) {
        if (live) setError(e.message);
      }
    };
    const t = setTimeout(run, 350);
    return () => {
      live = false;
      clearTimeout(t);
    };
    // Values that did not read are tried again as they are corrected.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fillKey, error ? values : ""]);

  const handEdited = (keys: string[]) =>
    keys.length > 0 && (
      <div className="field-hint cat-kept" title={keys.join(", ")}>
        ✎ {keys.map((k) => k.split(".").pop()).join(", ")} changed in the values by hand — left as you wrote it
      </div>
    );
  const chartSays = (path?: string) =>
    path && docs[path] ? (
      <div className="cat-chart-says" title={path}>
        Chart: {docs[path]}
      </div>
    ) : null;
  const keptOf = (paths: string[]) => kept.filter((k) => paths.includes(k));

  const hasQuestions =
    install &&
    (settings.ingresses.length > 0 ||
      settings.storage.length > 0 ||
      settings.disks.length > 0 ||
      settings.presets.length > 0 ||
      settings.replicas.length > 0);

  return (
    <div className="cat-form">
      <ChartNeeds
        asked={asked}
        stillAsked={stillAsked}
        refusal={refusal}
        typed={typed}
        setTyped={setTyped}
        extra={extra}
        setExtra={setExtra}
        docs={docs}
        examples={info.examples ?? {}}
        secrets={hints?.secrets}
        kept={kept}
      />

      {hasQuestions && <div className="cat-side-title">Questions</div>}

      {install && settings.ingresses.length > 0 && hints && (
        <div className="cat-question">
          <label className="checkbox cat-q">
            <input type="checkbox" checked={expose} onChange={(e) => setExpose(e.target.checked)} /> Open it in a browser
          </label>
          <div className="cat-why">
            Gives it an address people can open, through the cluster's ingress. Leave it off when only other services
            in the cluster talk to it.
          </div>
          {expose && (
            <div className="cat-ingress">
              {settings.ingresses.length > 1 && (
                <div className="field">
                  <label className="muted">Which part of the chart</label>
                  <select className="input mono" value={spot} onChange={(e) => setSpot(Number(e.target.value))}>
                    {settings.ingresses.map((s, i) => (
                      <option key={s.path + s.shape} value={i}>
                        {s.shape === "domain" ? "all of it, by domain" : s.path}
                      </option>
                    ))}
                  </select>
                </div>
              )}
              <div className="field">
                <label className="muted">{domainShape ? "Domain" : "Address"}</label>
                <input
                  className="input mono"
                  value={host}
                  placeholder={domainShape ? "dev.example.com" : "app.example.com"}
                  onChange={(e) => {
                    setHostTouched(true);
                    setHost(e.target.value.trim());
                  }}
                />
                {domainShape && (
                  <div className="field-hint">The chart names each of its parts in it: {info.name}.{host || "…"} and so on.</div>
                )}
                {!host && <div className="field-hint">Type an address to add the ingress.</div>}
              </div>
              <div className="field">
                <label className="muted">Ingress class</label>
                <select className="input" value={ingressClass} onChange={(e) => setIngressClass(e.target.value)}>
                  <option value="">the cluster's default</option>
                  {Array.from(new Set([...hints.ingressClasses, hints.ingressClass].filter(Boolean))).map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label className="muted">HTTPS certificate</label>
                <select className="input" value={cert} onChange={(e) => setTls(e.target.value)}>
                  <option value="">none — plain http</option>
                  {certs.map((c) => (
                    <option key={c.name} value={c.name}>
                      {c.name}
                      {covers(c.names, probe) ? " ✓ covers the address" : ""} · {c.days} d left
                    </option>
                  ))}
                </select>
                {certs.length === 0 && (
                  <div className="field-hint">No certificates in this namespace yet — it opens over plain http.</div>
                )}
                {cert && host && !covers(certs.find((c) => c.name === cert)?.names ?? [], probe) && (
                  <div className="field-hint cat-kept">This certificate does not cover {host}.</div>
                )}
              </div>
              {handEdited(kept.filter((k) => (domainShape ? k.startsWith("global.") : k.startsWith(sp.path + "."))))}
            </div>
          )}
        </div>
      )}

      {install && (settings.storage.length > 0 || settings.disks.length > 0) && (
        <div className="cat-question">
          <div className="cat-q">Keep data across restarts</div>
          <div className="cat-why">Where its data lives, so it survives a restart. The cluster's default is fine unless you were told otherwise.</div>
          {settings.storage.length > 0 && hints && (
            <div className="field">
              <label className="muted" title={settings.storage.join(", ")}>
                Storage class
              </label>
              <select className="input" value={storage} onChange={(e) => setStorage(e.target.value)}>
                <option value="">{defaultClass ? `cluster default (${defaultClass})` : "as the chart has it"}</option>
                {hints.storageClasses.map((c) => (
                  <option key={c.name} value={c.name}>
                    {c.name}
                    {c.default ? " — default" : ""}
                  </option>
                ))}
              </select>
              {handEdited(keptOf(settings.storage))}
            </div>
          )}
          {settings.disks.length > 0 && (
            <div className="field">
              <label className="muted" title={settings.disks.join(", ")}>
                Disk size{settings.disks.length > 1 ? ` · each of ${settings.disks.length} volumes` : ""}
              </label>
              <input
                className="input mono"
                value={disk}
                placeholder={settings.defaults[settings.disks[0]] ? `${settings.defaults[settings.disks[0]]} (the chart's)` : "8Gi"}
                onChange={(e) => setDisk(e.target.value.trim())}
              />
              {disk && !diskOK && <div className="field-hint cat-kept">A number with Mi, Gi or Ti, like 20Gi.</div>}
              {handEdited(keptOf(settings.disks))}
            </div>
          )}
        </div>
      )}

      {install && settings.presets.length > 0 && (
        <div className="cat-question">
          <div className="cat-q">How big</div>
          <div className="cat-why">CPU and memory each pod may use. Small is enough to try it out; pick more for real load.</div>
          <select className="input" value={preset} onChange={(e) => setPreset(e.target.value)}>
            <option value="">the chart's ({settings.defaults[settings.presets[0]] || "default"})</option>
            {PRESETS.map(([p, what]) => (
              <option key={p} value={p}>
                {p} — {what}
              </option>
            ))}
          </select>
          {chartSays(settings.presets[0])}
          {handEdited(keptOf(settings.presets))}
        </div>
      )}

      {install && settings.replicas.length > 0 && (
        <div className="cat-question">
          <div className="cat-q">How many copies</div>
          <div className="cat-why">How many pods run at once. More survive a node going down, and cost more.</div>
          <div className="cat-replicas">
            {settings.replicas.map((p) => (
              <div className="field" key={p}>
                <label className="muted mono" title={docs[p] || p}>
                  {p.includes(".") ? p.slice(0, p.lastIndexOf(".")) : "pods"}
                </label>
                <input
                  className="input mono"
                  inputMode="numeric"
                  value={replicas[p] ?? ""}
                  placeholder={settings.defaults[p] ?? ""}
                  onChange={(e) => setReplicas((r) => ({ ...r, [p]: e.target.value }))}
                />
              </div>
            ))}
          </div>
          {handEdited(keptOf(settings.replicas))}
        </div>
      )}

      {install && !hasQuestions && asked.length === 0 && (
        <div className="field-hint">
          This chart has nothing the portal knows how to ask about — it installs as its author ships it, or with what you
          write in Values (YAML). Once it runs, it can be opened from Services → Expose.
        </div>
      )}
      {error && <div className="cat-failed cat-settings-error">{error}</div>}
    </div>
  );
}

const secretKeyRE = /(^|\.)(secret|secretName|existingSecret)$/;
const urlRE = /(https?:\/\/[^\s)`'"]+)/g;

/** A sentence with its addresses as links. */
function Linked({ text }: { text: string }) {
  const parts = text.split(urlRE);
  return (
    <>
      {parts.map((p, i) =>
        i % 2 === 1 ? (
          <a key={i} href={p.replace(/[.,;]+$/, "")} target="_blank" rel="noopener noreferrer">
            {p}
          </a>
        ) : (
          <span key={i}>{p}</span>
        )
      )}
    </>
  );
}

const parentOf = (k: string) => (k.includes(".") ? k.slice(0, k.lastIndexOf(".")) : "");
const lastOf = (k: string) => k.slice(k.lastIndexOf(".") + 1);

/** What the chart needs before it installs: its own words when it refused,
    a field for each key it named, the key inside a Secret it takes by name,
    settings named in its words, and — folded — the optional settings beside
    the ones it asked for, as its values.yaml shows them commented out. */
function ChartNeeds({
  asked,
  stillAsked,
  refusal,
  typed,
  setTyped,
  extra,
  setExtra,
  docs,
  examples,
  secrets,
  kept,
}: {
  asked: { key: string; said: string }[];
  stillAsked: Set<string>;
  refusal?: { where?: string; messages: string[] };
  typed: Record<string, string>;
  setTyped: (f: (t: Record<string, string>) => Record<string, string>) => void;
  extra: Record<string, string>;
  setExtra: (f: (t: Record<string, string>) => Record<string, string>) => void;
  docs: Record<string, string>;
  examples: Record<string, string>;
  secrets?: { name: string; keys: string[] }[];
  kept: string[];
}) {
  const known = (k: string) => k in examples || k in docs;
  const shown = new Set(asked.map((a) => a.key));

  // The key inside each Secret the chart takes by name.
  const keyFields: string[] = [];
  for (const a of asked) {
    if (!secretKeyRE.test(a.key)) continue;
    const k = parentOf(a.key) + ".key";
    if (known(k) && !shown.has(k)) {
      keyFields.push(k);
      shown.add(k);
    }
  }
  // Settings named in the chart's words: `connection` → …connection.secret.
  const named: string[] = [];
  for (const m of refusal?.messages ?? []) {
    for (const [, word] of m.matchAll(/`([A-Za-z][\w-]*)`/g)) {
      for (const k of Object.keys(examples)) {
        if (named.length >= 6) break;
        if (k.split(".").includes(word) && !shown.has(k)) {
          named.push(k);
          shown.add(k);
        }
      }
    }
  }
  // A Secret's name before the key inside it.
  named.sort((a, b) =>
    parentOf(a) === parentOf(b) ? Number(lastOf(a) === "key") - Number(lastOf(b) === "key") : a.localeCompare(b)
  );
  // Beside what was asked: the rest of the block, as the chart shows it.
  const groups: string[] = [];
  for (const a of asked) {
    let g = parentOf(a.key);
    if (secretKeyRE.test(a.key) || /^(password|auth|credentials)$/.test(lastOf(g))) g = parentOf(g);
    if (g && !groups.includes(g)) groups.push(g);
  }
  const optional = (g: string) =>
    Object.keys(examples).filter(
      (k) => parentOf(k) === g && !shown.has(k) && !Object.keys(examples).some((x) => x.startsWith(k + "."))
    );

  const value = (k: string) => typed[k] ?? extra[k] ?? "";
  const field = (k: string, required: boolean) => {
    const secretish = secretKeyRE.test(k);
    const inSecret = !secretish && lastOf(k) === "key" ? secrets?.find((s) => s.name === value(parentOf(k) + ".secret")) : undefined;
    const list = secretish ? (secrets ?? []).map((s) => s.name) : (inSecret?.keys ?? []);
    const said = docs[k] || (required ? asked.find((a) => a.key === k)?.said : "");
    const set = required ? setTyped : setExtra;
    return (
      <div className="field" key={k}>
        <label className="mono cat-asked-key">
          {k}
          {!required && <span className="cat-dim"> · optional</span>}
        </label>
        {said && <div className="cat-why">{said}</div>}
        {secretish && (
          <div className="cat-why">
            The name of a Secret in this namespace that holds it — not the value itself.
            {secrets && secrets.length === 0 && " There are none here yet: create one under Secrets & ConfigMaps."}
          </div>
        )}
        {lastOf(k) === "key" && !secretish && k.endsWith(".key") && (
          <div className="cat-why">The key inside that Secret.</div>
        )}
        <input
          className="input"
          list={list.length ? "dl-" + k : undefined}
          value={value(k)}
          placeholder={required && stillAsked.has(k) ? "required" : examples[k] ? `e.g. ${examples[k]}` : ""}
          onChange={(e) => set((t) => ({ ...t, [k]: e.target.value }))}
        />
        {list.length > 0 && (
          <datalist id={"dl-" + k}>
            {list.map((n) => (
              <option key={n} value={n} />
            ))}
          </datalist>
        )}
        {kept.includes(k) && (
          <div className="field-hint cat-kept">✎ changed in the values by hand — left as you wrote it</div>
        )}
      </div>
    );
  };

  if (asked.length === 0 && !refusal) return null;
  const open = !!refusal || asked.some((a) => stillAsked.has(a.key) && !typed[a.key]?.trim());
  return (
    <div className={"cat-asked" + (open ? " open" : "")}>
      <div className="cat-q">The chart needs this to install</div>
      {refusal && refusal.messages.length > 0 && (
        <div className="cat-says">
          <div className="cat-why">The chart says:</div>
          <ul>
            {refusal.messages.map((m, i) => (
              <li key={i}>
                <Linked text={m} />
              </li>
            ))}
          </ul>
          {refusal.where && <div className="cat-dim mono">at {refusal.where}</div>}
        </div>
      )}
      {asked.map((a) => [field(a.key, true), ...keyFields.filter((k) => parentOf(k) === parentOf(a.key)).map((k) => field(k, false))])}
      {named.length > 0 && (
        <>
          <div className="cat-why">Settings of this chart the message names:</div>
          {named.map((k) => field(k, false))}
        </>
      )}
      {groups.map((g) => {
        const keys = optional(g);
        if (keys.length === 0) return null;
        return (
          <details className="cat-more" key={g}>
            <summary>
              More for <span className="mono">{g}</span>: {keys.map(lastOf).join(", ")}
            </summary>
            {keys.map((k) => field(k, false))}
          </details>
        );
      })}
    </div>
  );
}

// Bitnami's presets, by what each lets a pod use at most.
const PRESETS: [string, string][] = [
  ["nano", "up to 0.15 CPU, 192 MiB"],
  ["micro", "up to 0.375 CPU, 384 MiB"],
  ["small", "up to 0.75 CPU, 768 MiB"],
  ["medium", "up to 0.75 CPU, 1.5 GiB"],
  ["large", "up to 1.5 CPU, 3 GiB"],
  ["xlarge", "up to 3 CPU, 6 GiB"],
  ["2xlarge", "up to 6 CPU, 12 GiB"],
];

/** An administrator keeping the current values as everyone's start. */
function TemplateAdmin({
  repo,
  chart,
  values,
  template,
  onChanged,
}: {
  repo: string;
  chart: string;
  values: string;
  template?: HelmTemplate;
  onChanged: (t?: HelmTemplate) => void;
}) {
  const api = useApi();
  const [open, setOpen] = useState(false);
  const [note, setNote] = useState(template?.note ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      onChanged(await api.saveTemplate(repo, chart, values, note));
      setOpen(false);
      setDone("Saved — everyone installing this chart starts from these values.");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  const drop = async () => {
    setBusy(true);
    setError("");
    try {
      await api.deleteTemplate(repo, chart);
      onChanged(undefined);
      setDone("Removed — the chart starts from its own defaults again.");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="cat-template-admin">
      <div className="cat-side-title">Starting values for everyone</div>
      <div className="cat-why">
        {template
          ? `Everyone installing ${chart} starts from the values ${template.author} saved ${ago(template.updated)}.`
          : `Save these values — your domain, registry, the email a certificate needs — and everyone installing ${chart} starts from them.`}{" "}
        Anything in them is shown to whoever installs the chart.
      </div>
      {open ? (
        <div className="cat-template-form">
          <input
            className="input"
            value={note}
            placeholder="A note for them, e.g. 'our domain and mirror'"
            onChange={(e) => setNote(e.target.value)}
          />
          <div className="row" style={{ gap: 8 }}>
            <button className="btn sm" disabled={busy || !values.trim()} onClick={save}>
              Save these values
            </button>
            <button className="btn ghost sm" onClick={() => setOpen(false)}>
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
          <button className="btn ghost sm" disabled={busy || !values.trim()} onClick={() => setOpen(true)}>
            {template ? "Replace with the current values" : "Save the current values"}
          </button>
          {template && (
            <button className="btn ghost sm" disabled={busy} onClick={drop}>
              Remove
            </button>
          )}
        </div>
      )}
      {done && <div className="field-hint">{done}</div>}
      {error && <div className="cat-failed cat-settings-error">{error}</div>}
    </div>
  );
}

/** The check in one line, beside the button it decides: the verdict, what
    would be created, and how many problems — a click lists them. */
function CheckLine({
  check,
  checking,
  since,
  error,
  open,
  onToggle,
}: {
  check?: CatalogCheck;
  checking: boolean;
  since: number;
  error?: string;
  open: boolean;
  onToggle: () => void;
}) {
  const elapsed = useElapsed(checking ? since : 0);
  const slow = elapsed >= 4 ? ` ${elapsed} s — a large chart takes a while` : "";
  if (error) return <span className="cat-line err">⚠ {error}</span>;
  if (!check)
    return (
      <span className="cat-line muted">
        {checking ? (
          <>
            <span className="cat-spin" aria-hidden="true" /> Checking against the cluster…{slow}
          </>
        ) : (
          "Not checked yet"
        )}
      </span>
    );
  const errors = check.problems.filter((p) => p.level === "error").length;
  const warns = check.problems.length - errors;
  const found = check.imageStates.filter((s) => !s.missing && !s.unknown).length;
  const state = !check.ok ? "err" : warns > 0 ? "warn" : "ok";
  return (
    <span className={"cat-line " + state + (checking ? " stale" : "")}>
      <span className="cat-line-state">
        {state === "err"
          ? check.required?.length
            ? "✕ The chart needs a value"
            : "✕ Cannot be installed"
          : state === "warn"
            ? "⚠ Can be installed"
            : "✓ Check passed"}
      </span>
      <span className="muted" title={objectSummary(check.objects)}>
        {check.objects.length} {check.objects.length === 1 ? "object" : "objects"}
        {check.imageStates.length > 0 && ` · ${found}/${check.imageStates.length} images`}
        {check.newNamespace && " · new namespace"}
      </span>
      {check.problems.length > 0 && (
        <button className="cat-line-toggle" onClick={onToggle} aria-expanded={open}>
          {[errors > 0 && `${errors} ${errors === 1 ? "error" : "errors"}`, warns > 0 && `${warns} ${warns === 1 ? "warning" : "warnings"}`]
            .filter(Boolean)
            .join(", ")}{" "}
          {open ? "▾" : "▴"}
        </button>
      )}
      {checking && (
        <span className="muted cat-rechecking">
          <span className="cat-spin" aria-label="Checking again" />
          {slow && ` checking again…${slow}`}
        </span>
      )}
    </span>
  );
}

/** Whole seconds since a moment, ticking; 0 when there is none. */
function useElapsed(since: number): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!since) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [since]);
  return since ? Math.max(0, Math.floor((now - since) / 1000)) : 0;
}

// Lines lit up for a moment after the questions wrote them.
const flashLines = StateEffect.define<number[]>();
const flashMark = Decoration.line({ class: "cm-filled" });
const flashField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(deco, tr) {
    deco = deco.map(tr.changes);
    for (const e of tr.effects) {
      if (!e.is(flashLines)) continue;
      const b = new RangeSetBuilder<Decoration>();
      for (const n of [...e.value].sort((a, z) => a - z)) {
        if (n >= 1 && n <= tr.state.doc.lines) {
          const at = tr.state.doc.line(n).from;
          b.add(at, at, flashMark);
        }
      }
      deco = b.finish();
    }
    return deco;
  },
  provide: (f) => EditorView.decorations.from(f),
});

/** The lines of after that were not in before. */
function changedLines(before: string, after: string): number[] {
  const left = new Map<string, number>();
  for (const l of before.split("\n")) left.set(l, (left.get(l) ?? 0) + 1);
  const out: number[] = [];
  after.split("\n").forEach((l, i) => {
    const n = left.get(l) ?? 0;
    if (n > 0) left.set(l, n - 1);
    else if (l.trim()) out.push(i + 1);
  });
  return out;
}

/** Does a certificate with these names cover host? */
function covers(names: string[], host: string): boolean {
  const rest = host.slice(host.indexOf(".") + 1);
  return names.some((n) => n === host || (n.startsWith("*.") && host.includes(".") && rest === n.slice(2)));
}
