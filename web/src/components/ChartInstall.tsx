import { useEffect, useMemo, useRef, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { useApi } from "../useApi";
import { useEditorTheme } from "../editorTheme";
import { CatalogError, type CatalogAsk, type CatalogChart, type CatalogCheck, type HelmRequest } from "../api";
import { CheckResult } from "./Catalog";
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
      <div className="modal cat-install" onClick={(e) => e.stopPropagation()}>
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
          <button className="icon-btn" onClick={props.onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="modal-body pad">
          {loadError && <div className="alert">{loadError}</div>}
          {!info && !loadError && <div className="muted">Fetching the chart…</div>}
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
                  Your values
                </button>
                <button className={tab === "defaults" ? "on" : ""} onClick={() => setTab("defaults")}>
                  Chart defaults
                </button>
                <button className={tab === "readme" ? "on" : ""} onClick={() => setTab("readme")}>
                  README
                </button>
              </div>
              {tab === "values" && (
                <>
                  <div className="field-hint">
                    {kind === "upgrade"
                      ? "What this release was given last time, on top of the chart's defaults. Change what you need."
                      : "Only what differs from the chart's defaults. Empty installs the chart as its author ships it — passwords it needs are generated by the chart itself."}
                  </div>
                  <CodeMirror
                    value={values}
                    theme={theme}
                    height="260px"
                    extensions={[yaml()]}
                    placeholder={"# e.g.\n# replicaCount: 2\n# resources:\n#   limits: { memory: 512Mi }"}
                    onChange={setValues}
                  />
                </>
              )}
              {tab === "defaults" && (
                <CodeMirror value={info.defaults} theme={theme} height="290px" extensions={[yaml()]} editable={false} />
              )}
              {tab === "readme" && (
                <div className="cat-readme cat-readme-md">
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
