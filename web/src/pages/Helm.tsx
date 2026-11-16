import { Suspense, lazy, useCallback, useEffect, useRef, useState } from "react";
import { useParams, Link, useSearchParams } from "react-router-dom";
import { useApi } from "../useApi";
import type { CatalogPackage, HelmRelease, HelmRequest } from "../api";
import { useDialogs } from "../components/Dialogs";
import { pushToast } from "../toasts";
import { RequestStatus, requestLine, requestNote } from "../components/Catalog";

// The install dialog carries the YAML editor; loaded when it is opened.
const ChartInstall = lazy(() => import("../components/ChartInstall").then((m) => ({ default: m.ChartInstall })));

type Access = { install: boolean; upgrade: boolean; direct: boolean };

/** What the install dialog is open for. */
type Installing = {
  kind: "install" | "upgrade";
  repo: string;
  chart: string;
  release?: string;
  fromVersion?: string;
  values?: string;
  version?: string;
};

type View = "releases" | "catalog" | "requests";

export function Helm() {
  const { namespace = "" } = useParams();
  const api = useApi();
  const [releases, setReleases] = useState<HelmRelease[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  // History / rollback dialog
  const [historyFor, setHistoryFor] = useState<string | null>(null);
  const [history, setHistory] = useState<HelmRelease[]>([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyError, setHistoryError] = useState("");
  const [busy, setBusy] = useState(false);
  const { dialogs, confirm, alertDlg } = useDialogs();

  // The catalog's tabs, kept in the address so a link opens the right one.
  const [params, setParams] = useSearchParams();
  const view = (params.get("view") as View) || "releases";
  const setView = (v: View) => setParams(v === "releases" ? {} : { view: v }, { replace: true });
  // Null until known, and where the catalog is switched off.
  const [access, setAccess] = useState<Access | null>(null);
  const [installing, setInstalling] = useState<Installing | null>(null);

  const apiRef = useRef(api);
  apiRef.current = api;

  const load = useCallback(() => {
    setLoading(true);
    apiRef.current
      .releases(namespace)
      .then((d) => {
        setReleases(d);
        setError("");
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [namespace]);

  useEffect(load, [load]);

  useEffect(() => {
    apiRef.current
      .catalogAccess(namespace)
      .then(setAccess)
      .catch(() => setAccess(null));
  }, [namespace]);

  const openHistory = async (name: string) => {
    setHistoryFor(name);
    setHistory([]);
    setHistoryError("");
    setHistoryLoading(true);
    try {
      const h = await api.releaseHistory(namespace, name);
      setHistory([...h].sort((a, b) => b.revision - a.revision)); // newest first
    } catch (e: any) {
      setHistoryError(e.message);
    } finally {
      setHistoryLoading(false);
    }
  };

  const rollback = async (name: string, revision: number) => {
    const ok = await confirm({
      title: "Roll back release",
      message: `Roll ${name} back to revision ${revision}?`,
      okText: "Roll back",
    });
    if (!ok) return;
    setBusy(true);
    try {
      await api.rollback(namespace, name, revision);
      setHistoryFor(null);
      load();
      // The rollback itself is applied immediately; the pods take a while to
      // come up, which is why this says "restarting" rather than "done".
      pushToast(`${name} rolled back to revision ${revision} — pods are restarting`, "ok");
    } catch (e: any) {
      void alertDlg(e.message, "Error");
    } finally {
      setBusy(false);
    }
  };

  const uninstall = async (name: string) => {
    const ok = await confirm({
      title: "Uninstall release",
      message: `Uninstall ${name}? This cannot be undone.`,
      okText: "Uninstall",
      danger: true,
    });
    if (!ok) return;
    try {
      const res = await api.uninstall(namespace, name);
      load();
      // Helm may remove the release while failing to delete some of its
      // resources — that is a warning, not an error.
      if (res.warning) void alertDlg(res.warning, "Uninstalled, with a caveat");
      else pushToast(`Release ${name} uninstalled`, "ok");
    } catch (e: any) {
      void alertDlg(e.message, "Error");
    }
  };

  const startUpgrade = async (r: HelmRelease) => {
    try {
      const cur = await api.catalogRelease(namespace, r.name);
      setInstalling({
        kind: "upgrade",
        repo: cur.origin.repo,
        chart: cur.origin.chart,
        release: r.name,
        fromVersion: cur.version,
        values: cur.values,
      });
    } catch (e: any) {
      void alertDlg(e.message, "Error");
    }
  };

  const requestSent = (q: HelmRequest) => {
    setInstalling(null);
    pushToast(
      q.status === "installing"
        ? `${q.release}: ${q.kind === "upgrade" ? "upgrading" : "installing"} — follow it under Requests`
        : `Request for ${q.release} sent to platform administrators`,
      "ok"
    );
    setView("requests");
  };

  const currentRevision = releases.find((r) => r.name === historyFor)?.revision;
  const tabs: View[] = access
    ? (["releases", "catalog", "requests"] as View[]).filter((v) => v !== "catalog" || access.install)
    : [];

  return (
    <div className="page">
      {dialogs}
      <div className="page-head">
        <div>
          <Link to={`/ns/${namespace}/pods`} className="crumb">
            ← {namespace}
          </Link>
          <h1 className="page-title">Helm releases</h1>
          {access && (access.install || access.upgrade) && (
            <div className="seg-switch" role="tablist">
              {tabs.map((v) => (
                <button key={v} className={view === v ? "on" : ""} onClick={() => setView(v)}>
                  {v === "releases" ? "Releases" : v === "catalog" ? "Catalog" : "Requests"}
                </button>
              ))}
            </div>
          )}
        </div>
        {view === "releases" && (
          <button className="btn ghost" onClick={load}>
            Refresh
          </button>
        )}
      </div>

      {installing && (
        <Suspense fallback={null}>
          <ChartInstall
            namespace={namespace}
            repo={installing.repo}
            chart={installing.chart}
            kind={installing.kind}
            release={installing.release}
            fromVersion={installing.fromVersion}
            initialValues={installing.values}
            initialVersion={installing.version}
            direct={!!access?.direct}
            onClose={() => setInstalling(null)}
            onDone={requestSent}
          />
        </Suspense>
      )}

      {view === "catalog" && access?.install && (
        <CatalogList
          namespace={namespace}
          onPick={(p) => setInstalling({ kind: "install", repo: p.repo, chart: p.name })}
        />
      )}

      {view === "requests" && access && (
        <RequestList
          namespace={namespace}
          onSendAgain={(q) =>
            setInstalling({
              kind: q.kind,
              repo: q.repo,
              chart: q.chart,
              release: q.release,
              fromVersion: q.fromVersion,
              values: q.values,
              version: q.version,
            })
          }
          alertDlg={alertDlg}
          confirm={confirm}
        />
      )}

      {view === "releases" && (
        <>
          {error && <div className="alert">{error}</div>}

          <div className="card no-pad">
            <table className="table">
              <thead>
                <tr>
                  <th>Release</th>
                  <th>Revision</th>
                  <th>Status</th>
                  <th>Chart</th>
                  <th>Version</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {releases.map((r) => (
                  <tr key={r.name}>
                    <td className="mono">
                      {r.name}
                      {r.catalog && (
                        <span className="tag" title={`Installed from the catalog: ${r.catalog.repo}/${r.catalog.chart}`}>
                          catalog
                        </span>
                      )}
                    </td>
                    <td>{r.revision}</td>
                    <td>{r.status}</td>
                    <td className="muted">{r.chart}</td>
                    <td className="muted">{r.version}</td>
                    <td className="right">
                      {r.catalog && access?.upgrade && (
                        <button className="btn sm" onClick={() => startUpgrade(r)}>
                          Upgrade
                        </button>
                      )}
                      <button className="btn ghost sm" onClick={() => openHistory(r.name)}>
                        History
                      </button>
                      <button className="btn danger sm" onClick={() => uninstall(r.name)}>
                        Uninstall
                      </button>
                    </td>
                  </tr>
                ))}
                {!loading && releases.length === 0 && (
                  <tr>
                    <td colSpan={6} className="muted center">
                      No Helm releases in this namespace
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
            {loading && <div className="muted center pad">Loading…</div>}
          </div>
        </>
      )}

      {historyFor && (
        <div className="modal-overlay" onClick={() => !busy && setHistoryFor(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div>
                <div className="muted">Revision history</div>
                <div className="modal-title mono">{historyFor}</div>
              </div>
              <button className="icon-btn" onClick={() => setHistoryFor(null)} disabled={busy}>
                ✕
              </button>
            </div>

            {historyError && <div className="alert">{historyError}</div>}

            <div className="modal-body">
              {historyLoading ? (
                <div className="muted center pad">Loading history…</div>
              ) : (
                <table className="table">
                  <thead>
                    <tr>
                      <th>Revision</th>
                      <th>Status</th>
                      <th>Chart</th>
                      <th>Version</th>
                      <th>Updated</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {history.map((h) => {
                      const isCurrent = h.revision === currentRevision;
                      return (
                        <tr key={h.revision}>
                          <td>
                            {h.revision}
                            {isCurrent && <span className="tag">current</span>}
                          </td>
                          <td>{h.status}</td>
                          <td className="muted">{h.chart}</td>
                          <td className="muted">{h.version}</td>
                          <td className="muted">
                            {h.updated ? new Date(h.updated).toLocaleString() : "—"}
                          </td>
                          <td className="right">
                            <button
                              className="btn sm"
                              disabled={isCurrent || busy}
                              onClick={() => rollback(historyFor, h.revision)}
                            >
                              Roll back to this
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

/** The catalog: a search over Artifact Hub, a page at a time. */
function CatalogList({ namespace, onPick }: { namespace: string; onPick: (p: CatalogPackage) => void }) {
  const api = useApi();
  const [q, setQ] = useState("");
  const [shown, setShown] = useState("");
  const [items, setItems] = useState<CatalogPackage[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  // Searched a moment after typing stops.
  useEffect(() => {
    const t = setTimeout(() => setShown(q.trim()), 400);
    return () => clearTimeout(t);
  }, [q]);

  useEffect(() => {
    let live = true;
    setLoading(true);
    api
      .catalogSearch(namespace, shown, 0)
      .then((p) => {
        if (!live) return;
        setItems(p.packages);
        setTotal(p.total);
        setError("");
      })
      .catch((e) => live && setError(e.message))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [api, namespace, shown]);

  const more = async () => {
    setLoading(true);
    try {
      const p = await api.catalogSearch(namespace, shown, items.length);
      setItems((cur) => [...cur, ...p.packages]);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="card no-pad">
      <div className="cat-search pad">
        <input
          className="input"
          placeholder="Search charts: redis, postgresql, kafka"
          value={q}
          autoFocus
          onChange={(e) => setQ(e.target.value)}
        />
        <span className="muted">
          From Artifact Hub, verified publishers only{total ? ` · ${total} found` : ""}
        </span>
      </div>
      {error && (
        <div className="alert" style={{ margin: 16 }}>
          {error}
        </div>
      )}
      <div className="cat-list">
        {items.map((p) => (
          <div className="cat-row" key={p.repo + "/" + p.name}>
            <div className="cat-logo-box">{p.logo ? <img src={p.logo} alt="" loading="lazy" /> : <span>⎈</span>}</div>
            <div className="cat-main">
              <div>
                <b>{p.name}</b> <span className="cm-pill">{p.repoTitle}</span>
                {p.official && <span className="tag">official</span>}
                {p.deprecated && <span className="tag warn">deprecated</span>}
              </div>
              <div className="muted cat-desc-line">
                {p.description} · {p.version}
                {p.appVersion ? ` · app ${p.appVersion}` : ""} · ★ {p.stars}
              </div>
            </div>
            <button className="btn sm" onClick={() => onPick(p)}>
              Install
            </button>
          </div>
        ))}
        {!loading && !error && items.length === 0 && <div className="muted center pad">Nothing matches</div>}
      </div>
      {loading && <div className="muted center pad">Searching…</div>}
      {!loading && items.length < total && (
        <div className="center pad">
          <button className="btn ghost" onClick={more}>
            Show more
          </button>
        </div>
      )}
    </div>
  );
}

type ConfirmFn = (o: { title: string; message: string; okText?: string; danger?: boolean }) => Promise<boolean>;

/** This namespace's requests, newest first; refreshed while one is open. */
function RequestList({
  namespace,
  onSendAgain,
  alertDlg,
  confirm,
}: {
  namespace: string;
  onSendAgain: (q: HelmRequest) => void;
  alertDlg: (msg: string, title?: string) => Promise<unknown>;
  confirm: ConfirmFn;
}) {
  const api = useApi();
  const [list, setList] = useState<HelmRequest[] | null>(null);
  const [error, setError] = useState("");
  const [changesOf, setChangesOf] = useState<HelmRequest | null>(null);

  const load = useCallback(() => {
    api
      .namespaceRequests(namespace)
      .then((l) => {
        setList(l);
        setError("");
      })
      .catch((e) => setError(e.message));
  }, [api, namespace]);

  useEffect(load, [load]);
  const open = list?.some((q) => q.status === "installing" || q.status === "waiting");
  useEffect(() => {
    if (!open) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [open, load]);

  const withdraw = async (q: HelmRequest) => {
    const ok = await confirm({
      title: "Withdraw request",
      message: `Withdraw the request for ${q.release}?`,
      okText: "Withdraw",
    });
    if (!ok) return;
    try {
      await api.withdrawRequest(namespace, q.id);
      load();
    } catch (e: any) {
      void alertDlg(e.message, "Error");
    }
  };

  return (
    <div className="card no-pad">
      {error && (
        <div className="alert" style={{ margin: 16 }}>
          {error}
        </div>
      )}
      {list === null && !error && <div className="muted center pad">Loading…</div>}
      {list?.length === 0 && <div className="muted center pad">No requests in this namespace yet</div>}
      <div className="cat-list">
        {list?.map((q) => (
          <div className="cat-row" key={q.id}>
            <div className="cat-main">
              <div>
                <b className="mono">{q.release}</b> <span className="muted">{requestLine(q)}</span>
              </div>
              <div className={"cat-desc-line " + (q.status === "failed" ? "cat-failed" : "muted")}>{requestNote(q)}</div>
            </div>
            <RequestStatus r={q} />
            {q.status === "waiting" && (
              <button className="btn ghost sm" onClick={() => withdraw(q)}>
                Withdraw
              </button>
            )}
            {q.askedValues !== undefined && q.askedValues !== null && (
              <button className="btn ghost sm" onClick={() => setChangesOf(q)}>
                What changed
              </button>
            )}
            {["expired", "rejected", "failed", "withdrawn"].includes(q.status) && (
              <button className="btn ghost sm" onClick={() => onSendAgain(q)}>
                {q.status === "failed" ? "Try again" : "Send again"}
              </button>
            )}
          </div>
        ))}
      </div>
      {changesOf && (
        <div className="modal-overlay" onClick={() => setChangesOf(null)}>
          <div className="modal ing-editor" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div>
                <div className="muted">Changed on approval by {changesOf.decider}</div>
                <div className="modal-title mono">{changesOf.release}</div>
              </div>
              <button className="icon-btn" onClick={() => setChangesOf(null)} aria-label="Close">
                ✕
              </button>
            </div>
            <div className="modal-body pad ing-two">
              <div>
                <div className="muted">You asked for</div>
                <pre className="cat-readme">{changesOf.askedValues || "(chart defaults)"}</pre>
              </div>
              <div>
                <div className="muted">Installed with</div>
                <pre className="cat-readme">{changesOf.values || "(chart defaults)"}</pre>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
