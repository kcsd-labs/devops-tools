import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useApi } from "../useApi";
import type { CatalogPackage, HelmRelease, HelmRequest } from "../api";
import { useDialogs } from "../components/Dialogs";
import { pushToast } from "../toasts";
import { RequestStatus, requestLine, requestNote } from "../components/Catalog";

// Helm Packages: installing charts from the catalog, upgrading what was
// installed from it, and following the requests that ask for either. Not tied
// to a namespace: the catalog is one list, and what was installed or asked for
// is shown across every namespace the caller works in. The namespace is chosen
// in the install dialog.

// The install dialog carries the YAML editor, and the details a Markdown
// renderer; each is loaded when it is opened.
const ChartInstall = lazy(() => import("../components/ChartInstall").then((m) => ({ default: m.ChartInstall })));
const ChartDetails = lazy(() => import("../components/ChartDetails").then((m) => ({ default: m.ChartDetails })));

type Access = { install: boolean; upgrade: boolean; direct: boolean };
type View = "catalog" | "installed" | "requests";
type CatalogRelease = HelmRelease & { mayUpgrade: boolean; mayList: boolean };

/** What the install dialog is open for. */
type Installing = {
  kind: "install" | "upgrade";
  repo: string;
  chart: string;
  /** Fixed for an upgrade, and for a request sent again. */
  namespace?: string;
  release?: string;
  fromVersion?: string;
  values?: string;
  version?: string;
};

export function HelmPackages() {
  const api = useApi();
  const [params, setParams] = useSearchParams();
  const [error, setError] = useState("");
  const [access, setAccess] = useState<Access | null>(null);
  const [installing, setInstalling] = useState<Installing | null>(null);
  const [details, setDetails] = useState<CatalogPackage | null>(null);
  const { dialogs, confirm, alertDlg } = useDialogs();

  const tabs: View[] = access
    ? (["catalog", "installed", "requests"] as View[]).filter((v) => v !== "catalog" || access.install)
    : [];
  const wanted = params.get("view") as View | null;
  const view: View = wanted && tabs.includes(wanted) ? wanted : (tabs[0] ?? "catalog");
  const go = (v: View) => setParams({ view: v }, { replace: true });

  useEffect(() => {
    api
      .catalogAccess()
      .then(setAccess)
      .catch((e) => setError(e.message));
  }, [api]);

  const startUpgrade = async (r: HelmRelease) => {
    try {
      const cur = await api.catalogRelease(r.namespace, r.name);
      setInstalling({
        kind: "upgrade",
        repo: cur.origin.repo,
        chart: cur.origin.chart,
        namespace: r.namespace,
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
        ? `${q.release}: ${q.kind === "upgrade" ? "upgrading" : "installing"} in ${q.namespace} — follow it under Requests`
        : `Request for ${q.release} in ${q.namespace} sent to platform administrators`,
      "ok"
    );
    go("requests");
  };

  return (
    <div className="page">
      {dialogs}
      <div className="page-head">
        <div>
          <h1 className="page-title">Helm Packages</h1>
          {tabs.length > 0 && (
            <div className="seg-switch" role="tablist">
              {tabs.map((v) => (
                <button key={v} className={view === v ? "on" : ""} onClick={() => go(v)}>
                  {v === "catalog" ? "Catalog" : v === "installed" ? "Installed" : "Requests"}
                </button>
              ))}
            </div>
          )}
          <div className="muted svc-sub">
            Charts from Artifact Hub; the namespace is chosen when installing
            {access && !access.direct ? ", and a platform administrator approves" : ""}
          </div>
        </div>
      </div>

      {error && <div className="alert">{error}</div>}

      {installing && (
        <Suspense fallback={null}>
          <ChartInstall
            namespace={installing.namespace}
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

      {details && (
        <Suspense fallback={null}>
          <ChartDetails
            pkg={details}
            onClose={() => setDetails(null)}
            onInstall={() => {
              setDetails(null);
              setInstalling({ kind: "install", repo: details.repo, chart: details.name });
            }}
          />
        </Suspense>
      )}

      {access && view === "catalog" && (
        <CatalogList
          onOpen={setDetails}
          onPick={(p) => setInstalling({ kind: "install", repo: p.repo, chart: p.name })}
        />
      )}
      {access && view === "installed" && <InstalledList onUpgrade={startUpgrade} />}
      {access && view === "requests" && (
        <RequestList
          onSendAgain={(q) =>
            setInstalling({
              kind: q.kind,
              repo: q.repo,
              chart: q.chart,
              namespace: q.namespace,
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
    </div>
  );
}

/** The catalog: a search over Artifact Hub, a page at a time. */
function CatalogList({
  onOpen,
  onPick,
}: {
  onOpen: (p: CatalogPackage) => void;
  onPick: (p: CatalogPackage) => void;
}) {
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
      .catalogSearch(shown, 0)
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
  }, [api, shown]);

  const more = async () => {
    setLoading(true);
    try {
      const p = await api.catalogSearch(shown, items.length);
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
          <div
            className="cat-row clickable"
            key={p.repo + "/" + p.name}
            role="button"
            tabIndex={0}
            title="Show what this chart is"
            onClick={() => onOpen(p)}
            onKeyDown={(e) => e.key === "Enter" && onOpen(p)}
          >
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
            <button
              className="btn sm"
              onClick={(e) => {
                e.stopPropagation();
                onPick(p);
              }}
            >
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

/** What was installed from the catalog, in every namespace one works in. */
function InstalledList({ onUpgrade }: { onUpgrade: (r: HelmRelease) => void }) {
  const api = useApi();
  const [list, setList] = useState<CatalogRelease[] | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .catalogReleases()
      .then((l) => {
        setList(l);
        setError("");
      })
      .catch((e) => setError(e.message));
  }, [api]);

  return (
    <div className="card no-pad">
      {error && (
        <div className="alert" style={{ margin: 16 }}>
          {error}
        </div>
      )}
      {list === null && !error && <div className="muted center pad">Loading…</div>}
      {list?.length === 0 && <div className="muted center pad">Nothing was installed from the catalog yet</div>}
      <div className="cat-list">
        {list?.map((r) => (
          <div className="cat-row" key={r.namespace + "/" + r.name}>
            <div className="cat-main">
              <div>
                <b className="mono">{r.name}</b> <span className="cm-pill">{r.namespace}</span>{" "}
                <span className="muted">
                  {r.chart} {r.version} · {r.catalog?.repo}
                </span>
              </div>
              <div className="muted cat-desc-line">
                revision {r.revision} · {r.status}
                {r.updated ? ` · ${new Date(r.updated).toLocaleString()}` : ""}
              </div>
            </div>
            {r.mayList && (
              <Link className="btn ghost sm" to={`/ns/${r.namespace}/helm`}>
                History
              </Link>
            )}
            {r.mayUpgrade && (
              <button className="btn sm" onClick={() => onUpgrade(r)}>
                Upgrade
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

type ConfirmFn = (o: { title: string; message: string; okText?: string; danger?: boolean }) => Promise<boolean>;

/** The requests in every namespace one works in, newest first; refreshed
    while one is open. */
function RequestList({
  onSendAgain,
  alertDlg,
  confirm,
}: {
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
      .myRequests()
      .then((l) => {
        setList(l);
        setError("");
      })
      .catch((e) => setError(e.message));
  }, [api]);

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
      message: `Withdraw the request for ${q.release} in ${q.namespace}?`,
      okText: "Withdraw",
    });
    if (!ok) return;
    try {
      await api.withdrawRequest(q.namespace, q.id);
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
      {list?.length === 0 && <div className="muted center pad">No requests yet</div>}
      <div className="cat-list">
        {list?.map((q) => (
          <div className="cat-row" key={q.id}>
            <div className="cat-main">
              <div>
                <b className="mono">{q.release}</b> <span className="cm-pill">{q.namespace}</span>{" "}
                <span className="muted">{requestLine(q)}</span>
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
                <div className="modal-title mono">
                  {changesOf.release} · {changesOf.namespace}
                </div>
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
