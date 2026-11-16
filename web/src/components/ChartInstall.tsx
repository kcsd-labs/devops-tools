import { useEffect, useMemo, useRef, useState } from "react";
import CodeMirror, { EditorView } from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { useApi } from "../useApi";
import { useEditorTheme } from "../editorTheme";
import {
  CatalogError,
  type CatalogAsk,
  type CatalogChart,
  type CatalogCheck,
  type ClusterHints,
  type CatalogSettings,
  type HelmRequest,
} from "../api";
import { CheckResult, Loading } from "./Catalog";
import { Markdown } from "./Markdown";

// The namespace this browser last installed into, offered first next time.
const NAMESPACE_KEY = "devops-tools.helmPackages.namespace";

function rememberedNamespace(): string {
  try {
    return localStorage.getItem(NAMESPACE_KEY) ?? "";
  } catch {
    return "";
  }
}

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
  onClose: () => void;
  onDone: (r: HelmRequest) => void;
};

/** The dialog a chart is installed or upgraded from. */
export function ChartInstall(props: InstallProps) {
  const { repo, chart, kind, direct } = props;
  const fixedNamespace = !!props.namespace;
  const api = useApi();
  const theme = useEditorTheme();
  const [namespace, setNamespace] = useState(props.namespace ?? "");
  const [namespaces, setNamespaces] = useState<string[] | null>(fixedNamespace ? [props.namespace!] : null);
  const [nameTouched, setNameTouched] = useState(!!props.release);
  const [info, setInfo] = useState<CatalogChart | null>(null);
  const [loadError, setLoadError] = useState("");
  const [version, setVersion] = useState(props.initialVersion ?? "");
  const [release, setRelease] = useState(props.release ?? "");
  const [values, setValues] = useState(props.initialValues ?? "");
  const [tab, setTab] = useState<"values" | "defaults" | "readme">("values");
  const [check, setCheck] = useState<CatalogCheck>();
  const [checkedKey, setCheckedKey] = useState("");
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [busy, setBusy] = useState(false);
  const [submitError, setSubmitError] = useState("");
  // The whole window for the values, with the chart's defaults beside them.
  const [expanded, setExpanded] = useState(false);

  // Where it may go: the namespaces the caller may install into, the one
  // used last offered first.
  useEffect(() => {
    if (fixedNamespace) return;
    api
      .catalogNamespaces()
      .then((r) => {
        setNamespaces(r.namespaces);
        const last = rememberedNamespace();
        setNamespace((cur) => cur || (r.namespaces.includes(last) ? last : (r.namespaces[0] ?? "")));
      })
      .catch((e) => setLoadError(e.message));
  }, [api, fixedNamespace]);

  // The chart, for the chosen version; and a release name free in the
  // chosen namespace, unless one was typed.
  useEffect(() => {
    let live = true;
    setLoadError("");
    api
      .catalogChart(repo, chart, version, namespace)
      .then((c) => {
        if (!live) return;
        setInfo(c);
        if (!version) setVersion(c.version);
        if (kind === "install" && !nameTouched) setRelease(c.suggestedName);
      })
      .catch((e) => live && setLoadError(e.message));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, namespace, repo, chart, version]);

  const ask: CatalogAsk | null = useMemo(
    () => (info && version && release && namespace ? { kind, release, repo, chart, version, values } : null),
    [info, version, release, namespace, kind, repo, chart, values]
  );
  const key = ask ? JSON.stringify({ ...ask, namespace }) : "";

  // Checked again shortly after each change: what is installed is exactly
  // what was last checked.
  const seq = useRef(0);
  useEffect(() => {
    if (!ask) return;
    const n = ++seq.current;
    setChecking(true);
    const t = setTimeout(() => {
      api
        .catalogCheck(namespace, ask)
        .then((c) => {
          if (n !== seq.current) return;
          setCheck(c);
          setCheckedKey(key);
          setCheckError("");
        })
        .catch((e) => n === seq.current && (setCheckError(e.message), setCheck(undefined)))
        .finally(() => n === seq.current && setChecking(false));
    }, 700);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const ready = !!check?.ok && checkedKey === key && !checking && !busy;

  const submit = async () => {
    if (!ask) return;
    setBusy(true);
    setSubmitError("");
    try {
      const q = await api.catalogRequest(namespace, ask);
      try {
        localStorage.setItem(NAMESPACE_KEY, namespace);
      } catch {
        /* a convenience only */
      }
      props.onDone(q);
    } catch (e: any) {
      if (e instanceof CatalogError && e.check) setCheck(e.check);
      setSubmitError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const verb = kind === "upgrade" ? "Upgrade" : "Install";

  return (
    <div className="modal-overlay">
      <div className={"modal cat-install" + (expanded ? " expanded" : "")} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="cat-head">
            {info?.logo && <img className="cat-logo" src={info.logo} alt="" />}
            <div>
              <div className="muted">
                {kind === "upgrade" ? `${namespace} · upgrade ${props.release}` : "install from the catalog"}
              </div>
              <div className="modal-title">
                {chart} <span className="cm-pill">{info?.repoTitle ?? repo}</span>
              </div>
            </div>
          </div>
          <div className="row" style={{ gap: 8 }}>
            <button
              className="icon-btn"
              onClick={() => setExpanded((x) => !x)}
              aria-label={expanded ? "Make smaller" : "Use the whole window"}
              title={expanded ? "Make smaller" : "Use the whole window — values and chart defaults side by side"}
            >
              {expanded ? "⤡" : "⤢"}
            </button>
            <button className="icon-btn" onClick={props.onClose} aria-label="Close">
              ✕
            </button>
          </div>
        </div>
        <div className="modal-body pad">
          {loadError && <div className="alert">{loadError}</div>}
          {!info && !loadError && <Loading text="Fetching the chart…" bar slow="The chart is downloaded from where its author publishes it — a large one can take up to a minute." />}
          {namespaces?.length === 0 && (
            <div className="alert">There is no namespace you may install into. Ask for helm-install on one.</div>
          )}
          {info && (
            <>
              <div className="muted cat-desc">
                {info.description} <span className="mono">· {info.repoUrl}</span>
              </div>
              <div className="cat-fields">
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
                      value={namespace}
                      onChange={(e) => setNamespace(e.target.value)}
                      disabled={!namespaces?.length}
                    >
                      {(namespaces ?? []).map((ns) => (
                        <option key={ns} value={ns}>
                          {ns}
                        </option>
                      ))}
                    </select>
                  )}
                </div>
                <div className="field">
                  <label className="muted">
                    Version{props.fromVersion ? ` (now ${props.fromVersion})` : ""}
                  </label>
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
              </div>

              <div className="seg-switch cat-tabs">
                <button className={tab === "values" ? "on" : ""} onClick={() => setTab("values")}>
                  {expanded ? "Values and defaults" : "Your values"}
                </button>
                {!expanded && (
                  <button className={tab === "defaults" ? "on" : ""} onClick={() => setTab("defaults")}>
                    Chart defaults
                  </button>
                )}
                <button className={tab === "readme" ? "on" : ""} onClick={() => setTab("readme")}>
                  README
                </button>
              </div>
              {(tab === "values" || (expanded && tab === "defaults")) && (
                <div className={expanded ? "cat-split" : ""}>
                  <div>
                    {info.cluster && kind === "install" && (
                      <FillFromCluster
                        settings={info.settings}
                        hints={info.cluster}
                        release={release}
                        values={values}
                        onValues={setValues}
                      />
                    )}
                    {!info.cluster && kind === "install" && info.settings.storage.length + info.settings.ingresses.length === 0 && (
                      <div className="field-hint">
                        This chart has no storage class or ingress setting the portal recognises. Once it runs, its
                        service can be exposed from Services → Expose.
                      </div>
                    )}
                    <div className="field-hint">
                      {kind === "upgrade"
                        ? "What this release was given last time, on top of the chart's defaults. Change what you need."
                        : "Only what differs from the chart's defaults. Empty installs the chart as its author ships it — passwords it needs are generated by the chart itself."}
                    </div>
                    <CodeMirror
                      value={values}
                      theme={theme}
                      height={expanded ? "calc(100vh - 470px)" : "240px"}
                      extensions={[yaml()]}
                      placeholder={"# e.g.\n# replicaCount: 2\n# resources:\n#   limits: { memory: 512Mi }"}
                      onChange={setValues}
                    />
                  </div>
                  {expanded && (
                    <div>
                      <div className="field-hint">Chart defaults — read only; Ctrl+F searches</div>
                      <CodeMirror
                        value={info.defaults}
                        theme={theme}
                        height="calc(100vh - 290px)"
                        extensions={[yaml(), EditorView.lineWrapping]}
                        editable={false}
                      />
                    </div>
                  )}
                </div>
              )}
              {!expanded && tab === "defaults" && (
                <>
                  <div className="field-hint">Read only; Ctrl+F searches. ⤢ puts them beside your values.</div>
                  <CodeMirror
                    value={info.defaults}
                    theme={theme}
                    height="290px"
                    extensions={[yaml(), EditorView.lineWrapping]}
                    editable={false}
                  />
                </>
              )}
              {tab === "readme" && (
                <div className={"cat-readme cat-readme-md" + (expanded ? " tall" : "")}>
                  {info.readme ? <Markdown source={info.readme} /> : "The chart has no README."}
                </div>
              )}

              <CheckResult check={check} checking={checking} error={checkError} upgrade={kind === "upgrade"} />
              {submitError && <div className="alert" style={{ marginTop: 10 }}>{submitError}</div>}
            </>
          )}
        </div>
        <div className="modal-actions pad">
          <span className="muted cat-note">
            {direct
              ? "You're an administrator — this runs straight away and is written to the audit trail"
              : "🔒 A platform administrator approves this before anything is installed"}
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

/** Does a certificate with these names cover host? */
function covers(names: string[], host: string): boolean {
  const rest = host.slice(host.indexOf(".") + 1);
  return names.some((n) => n === host || (n.startsWith("*.") && host.includes(".") && rest === n.slice(2)));
}

/** What the cluster can fill in: a storage class, and an ingress for the
    chart's own ingress block. Written into the values on request, where it
    stays visible and can still be changed. */
function FillFromCluster({
  settings,
  hints,
  release,
  values,
  onValues,
}: {
  settings: CatalogSettings;
  hints: ClusterHints;
  release: string;
  values: string;
  onValues: (v: string) => void;
}) {
  const api = useApi();
  const defaultClass = hints.storageClasses.find((c) => c.default)?.name ?? "";
  const [storage, setStorage] = useState("");
  const [expose, setExpose] = useState(false);
  const [spot, setSpot] = useState(0);
  const [host, setHost] = useState("");
  const [hostTouched, setHostTouched] = useState(false);
  const [ingressClass, setIngressClass] = useState(hints.ingressClass);
  const [tls, setTls] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [added, setAdded] = useState(false);

  // The host follows the release name until someone types one.
  const suggested = hints.domain && release ? `${release}.${hints.domain}` : "";
  useEffect(() => {
    if (!hostTouched) setHost(suggested);
  }, [suggested, hostTouched]);
  // The certificate follows the host until someone picks one.
  const matching = hints.certs.find((c) => covers(c.names, host))?.name ?? "";
  const cert = tls ?? matching;

  if (settings.storage.length === 0 && settings.ingresses.length === 0) {
    return (
      <div className="field-hint">
        This chart has no storage class or ingress setting the portal recognises. Once it runs, its service can be
        exposed from Services → Expose.
      </div>
    );
  }

  const nothing = !storage && !expose;
  const apply = async () => {
    setError("");
    try {
      const sp = settings.ingresses[spot];
      const res = await api.fillValues(values, {
        storageClass: storage,
        storagePaths: settings.storage,
        ingress: expose && sp ? { ...sp, host, class: ingressClass, tls: cert } : undefined,
      });
      onValues(res.values);
      setAdded(true);
      setTimeout(() => setAdded(false), 2500);
    } catch (e: any) {
      setError(e.message);
    }
  };

  return (
    <div className="cat-fill">
      <div className="cat-fill-title">Fill from the cluster</div>
      {settings.storage.length > 0 && (
        <div className="cat-fill-row">
          <label className="muted">Storage class</label>
          <select className="input sm" value={storage} onChange={(e) => setStorage(e.target.value)}>
            <option value="">{defaultClass ? `cluster default (${defaultClass})` : "as the chart has it"}</option>
            {hints.storageClasses.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
                {c.default ? " — default" : ""}
              </option>
            ))}
          </select>
          <span className="muted cat-fill-note" title={settings.storage.join(", ")}>
            → {settings.storage.length === 1 ? settings.storage[0] : `${settings.storage.length} volumes`}
          </span>
        </div>
      )}
      {settings.ingresses.length > 0 && (
        <>
          <div className="cat-fill-row">
            <label className="checkbox">
              <input type="checkbox" checked={expose} onChange={(e) => setExpose(e.target.checked)} /> Open from outside
              through an ingress
            </label>
            {settings.ingresses.length > 1 && (
              <select
                className="input sm"
                value={spot}
                onChange={(e) => setSpot(Number(e.target.value))}
                disabled={!expose}
                aria-label="Which ingress of the chart"
              >
                {settings.ingresses.map((s, i) => (
                  <option key={s.path} value={i}>
                    {s.path}
                  </option>
                ))}
              </select>
            )}
          </div>
          {expose && (
            <div className="cat-fill-ingress">
              <input
                className="input sm mono"
                value={host}
                placeholder="app.example.com"
                onChange={(e) => {
                  setHostTouched(true);
                  setHost(e.target.value.trim());
                }}
                aria-label="Host"
              />
              <select
                className="input sm"
                value={ingressClass}
                onChange={(e) => setIngressClass(e.target.value)}
                aria-label="Ingress class"
              >
                <option value="">default class</option>
                {Array.from(new Set([...hints.ingressClasses, hints.ingressClass].filter(Boolean))).map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
              <select className="input sm" value={cert} onChange={(e) => setTls(e.target.value)} aria-label="Certificate">
                <option value="">no TLS</option>
                {hints.certs.map((c) => (
                  <option key={c.name} value={c.name}>
                    {c.name}
                    {covers(c.names, host) ? " ✓ covers the host" : ""} · {c.days} d
                  </option>
                ))}
              </select>
            </div>
          )}
        </>
      )}
      <div className="cat-fill-row">
        <button className="btn sm" disabled={nothing || (expose && !host)} onClick={apply}>
          Add to my values
        </button>
        {added && <span className="ing-state ok">added — see below</span>}
        {error && <span className="cat-failed">{error}</span>}
      </div>
    </div>
  );
}
