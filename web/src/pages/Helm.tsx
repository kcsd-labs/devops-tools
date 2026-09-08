import { useCallback, useEffect, useRef, useState } from "react";
import { useParams, Link } from "react-router-dom";
import { useApi } from "../useApi";
import type { HelmRelease } from "../api";
import { useDialogs } from "../components/Dialogs";
import { pushToast } from "../toasts";

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

  const currentRevision = releases.find((r) => r.name === historyFor)?.revision;

  return (
    <div className="page">
      {dialogs}
      <div className="page-head">
        <div>
          <Link to={`/ns/${namespace}/pods`} className="crumb">
            ← {namespace}
          </Link>
          <h1 className="page-title">Helm releases</h1>
        </div>
        <button className="btn ghost" onClick={load}>
          Refresh
        </button>
      </div>

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
                <td className="mono">{r.name}</td>
                <td>{r.revision}</td>
                <td>{r.status}</td>
                <td className="muted">{r.chart}</td>
                <td className="muted">{r.version}</td>
                <td className="right">
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
