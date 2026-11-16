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
  type CatalogSettings,
  type HelmRequest,
  type ValuesFill,
} from "../api";
import { Loading, ProblemList, objectSummary } from "./Catalog";
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
  // The tab being looked at, alone on the whole window.
  const [expanded, setExpanded] = useState(false);

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

  // What the settings on the left wrote, lit up in the editor for a moment.
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

  const noSettings = info && info.settings.storage.length + info.settings.ingresses.length === 0;

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
          <button className="icon-btn" onClick={props.onClose} aria-label="Close">
            ✕
          </button>
        </div>

        {(!info || namespaces?.length === 0) && (
          <div className="modal-body pad">
            {loadError && <div className="alert">{loadError}</div>}
            {!info && !loadError && (
              <Loading
                text="Fetching the chart…"
                bar
                slow="The chart is downloaded from where its author publishes it — a large one can take up to a minute."
              />
            )}
            {namespaces?.length === 0 && (
              <div className="alert">There is no namespace you may install into. Ask for helm-install on one.</div>
            )}
          </div>
        )}

        {info && (
          <div className="cat-body">
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

              {kind === "install" && (
                <>
                  <div className="cat-side-title">Settings</div>
                  {info.cluster && !noSettings && (
                    <ClusterSettings
                      settings={info.settings}
                      hints={info.cluster}
                      release={release}
                      values={values}
                      current={() => valuesRef.current}
                      onFilled={filled}
                    />
                  )}
                  {noSettings && (
                    <div className="field-hint">
                      This chart has no storage class or ingress setting the portal recognises. Once it runs, its
                      service can be opened from Services → Expose.
                    </div>
                  )}
                </>
              )}
            </aside>

            <section className="cat-main">
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
                </div>
                {expanded && (
                  <span className="muted mono cat-where">
                    {release} in {namespace} · {version}
                  </span>
                )}
                <button
                  className="btn ghost sm cat-expand"
                  onClick={() => setExpanded((x) => !x)}
                  title={expanded ? "Show the settings again (Esc)" : "Hide the settings, give the editor the whole window"}
                >
                  {expanded ? "↙ Show the settings" : "↗ Full screen"}
                </button>
              </div>
              <div className="field-hint cat-main-hint">
                {tab === "values" &&
                  (kind === "upgrade"
                    ? "What this release was given last time, on top of the chart's defaults. Change what you need."
                    : "Only what differs from the chart's defaults — the settings on the left write here too. Empty installs the chart as its author ships it.")}
                {tab === "defaults" && "Everything the chart can be given, with its defaults. Read only; Ctrl+F searches."}
                {tab === "readme" && info.repoUrl}
              </div>
              {tab === "values" && (
                <div className="cat-grow">
                  <CodeMirror
                    value={values}
                    theme={theme}
                    height="100%"
                    extensions={[yaml(), flashField]}
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
                    extensions={[yaml(), EditorView.lineWrapping]}
                    editable={false}
                  />
                </div>
              )}
              {tab === "readme" && (
                <div className="cat-readme cat-readme-md cat-grow">
                  {info.readme ? <Markdown source={info.readme} /> : "The chart has no README."}
                </div>
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
            error={checkError}
            open={showProblems}
            onToggle={() => setShowProblems((x) => !x)}
          />
          <span className="muted cat-note">
            {direct ? "You're an administrator: runs straight away, written to the audit trail" : "🔒 An administrator approves it first"}
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

/** The check in one line, beside the button it decides: the verdict, what
    would be created, and how many problems — a click lists them. */
function CheckLine({
  check,
  checking,
  error,
  open,
  onToggle,
}: {
  check?: CatalogCheck;
  checking: boolean;
  error?: string;
  open: boolean;
  onToggle: () => void;
}) {
  if (error) return <span className="cat-line err">⚠ {error}</span>;
  if (!check)
    return (
      <span className="cat-line muted">
        {checking ? (
          <>
            <span className="cat-spin" aria-hidden="true" /> Checking against the cluster…
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
        {state === "err" ? "✕ Cannot be installed" : state === "warn" ? "⚠ Can be installed" : "✓ Check passed"}
      </span>
      <span className="muted" title={objectSummary(check.objects)}>
        {check.objects.length} {check.objects.length === 1 ? "object" : "objects"}
        {check.imageStates.length > 0 && ` · ${found}/${check.imageStates.length} images`}
      </span>
      {check.problems.length > 0 && (
        <button className="cat-line-toggle" onClick={onToggle} aria-expanded={open}>
          {[errors > 0 && `${errors} ${errors === 1 ? "error" : "errors"}`, warns > 0 && `${warns} ${warns === 1 ? "warning" : "warnings"}`]
            .filter(Boolean)
            .join(", ")}{" "}
          {open ? "▾" : "▴"}
        </button>
      )}
      {checking && <span className="cat-spin" aria-label="Checking again" />}
    </span>
  );
}

// Lines lit up for a moment after the settings wrote them.
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

/** What the cluster can fill in — a storage class, an ingress — written into
    the values as each is chosen, and taken out again when it is unchosen. A
    key changed by hand in the editor since is left as it is. */
function ClusterSettings({
  settings,
  hints,
  release,
  values,
  current,
  onFilled,
}: {
  settings: CatalogSettings;
  hints: ClusterHints;
  release: string;
  values: string;
  /** The values now — they may change while a request is out. */
  current: () => string;
  onFilled: (v: string) => void;
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
  const [kept, setKept] = useState<string[]>([]);

  // The host follows the release name until someone types one.
  const suggested = hints.domain && release ? `${release}.${hints.domain}` : "";
  useEffect(() => {
    if (!hostTouched) setHost(suggested);
  }, [suggested, hostTouched]);
  // The certificate follows the host until someone picks one.
  const matching = hints.certs.find((c) => covers(c.names, host))?.name ?? "";
  const cert = tls ?? matching;

  const sp = settings.ingresses[spot];
  const fill: ValuesFill = {
    storageClass: storage,
    storagePaths: settings.storage,
    ingress: expose && sp && host ? { ...sp, host, class: ingressClass, tls: cert } : undefined,
  };
  const fillKey = JSON.stringify(fill);

  // What was last written, so the next change can take it out again.
  const previous = useRef<ValuesFill>({});
  const written = useRef(JSON.stringify({ storageClass: "", storagePaths: settings.storage }));
  useEffect(() => {
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
    const t = setTimeout(run, 300);
    return () => {
      live = false;
      clearTimeout(t);
    };
    // Values that did not read are tried again as they are corrected.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fillKey, error ? values : ""]);

  const keptStorage = kept.filter((k) => settings.storage.includes(k));
  const keptIngress = sp ? kept.filter((k) => k.startsWith(sp.path + ".")) : [];
  const handEdited = (keys: string[]) =>
    keys.length > 0 && (
      <div className="field-hint cat-kept" title={keys.join(", ")}>
        ✎ {keys.map((k) => k.split(".").pop()).join(", ")} changed in the values by hand — left as you wrote it
      </div>
    );

  return (
    <div className="cat-settings">
      {settings.storage.length > 0 && (
        <div className="field">
          <label className="muted" title={settings.storage.join(", ")}>
            Storage class{" "}
            <span className="cat-dim">
              · {settings.storage.length === 1 ? settings.storage[0] : `${settings.storage.length} volumes`}
            </span>
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
          {handEdited(keptStorage)}
        </div>
      )}

      {settings.ingresses.length > 0 && (
        <>
          <label className="checkbox cat-expose">
            <input type="checkbox" checked={expose} onChange={(e) => setExpose(e.target.checked)} /> Open from outside
            through an ingress
          </label>
          {expose && (
            <div className="cat-ingress">
              {settings.ingresses.length > 1 && (
                <div className="field">
                  <label className="muted">Which ingress of the chart</label>
                  <select className="input mono" value={spot} onChange={(e) => setSpot(Number(e.target.value))}>
                    {settings.ingresses.map((s, i) => (
                      <option key={s.path} value={i}>
                        {s.path}
                      </option>
                    ))}
                  </select>
                </div>
              )}
              <div className="field">
                <label className="muted">Host</label>
                <input
                  className="input mono"
                  value={host}
                  placeholder="app.example.com"
                  onChange={(e) => {
                    setHostTouched(true);
                    setHost(e.target.value.trim());
                  }}
                />
                {!host && <div className="field-hint">Type a host to add the ingress.</div>}
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
                <label className="muted">TLS certificate</label>
                <select className="input" value={cert} onChange={(e) => setTls(e.target.value)}>
                  <option value="">none — plain http</option>
                  {hints.certs.map((c) => (
                    <option key={c.name} value={c.name}>
                      {c.name}
                      {covers(c.names, host) ? " ✓ covers the host" : ""} · {c.days} d left
                    </option>
                  ))}
                </select>
                {cert && !covers(hints.certs.find((c) => c.name === cert)?.names ?? [], host) && host && (
                  <div className="field-hint cat-kept">This certificate does not cover {host}.</div>
                )}
              </div>
              {handEdited(keptIngress)}
            </div>
          )}
        </>
      )}
      {error && <div className="cat-failed cat-settings-error">{error}</div>}
    </div>
  );
}
