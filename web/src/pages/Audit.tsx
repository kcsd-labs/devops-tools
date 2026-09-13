import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { useApi } from "../useApi";
import type { AuditEntry, AuditPage } from "../api";

/** How far back this deployment looks, said in words rather than seconds. */
function horizonText(page: AuditPage | null): string {
  if (!page) return "";
  if (page.source === "pods") {
    return "read from this service's own pod logs — hours, until the node rotates them";
  }
  const days = Math.round(page.horizon / 86400);
  if (days >= 1) return `read from Loki — ${days} ${days === 1 ? "day" : "days"} back`;
  const hours = Math.round(page.horizon / 3600);
  return `read from Loki — ${hours} ${hours === 1 ? "hour" : "hours"} back`;
}

/** What this person is allowed to see, so an incomplete page does not read as
    a fault. Someone with two namespaces should know that is why. */
function scopeText(page: AuditPage | null): string {
  if (!page) return "";
  const parts: string[] = [];
  if (page.allNamespaces) parts.push("every namespace");
  else if (page.namespaces && page.namespaces.length > 0) parts.push(page.namespaces.join(", "));
  if (page.global) parts.push("sign-ins and access changes");
  return parts.length > 0 ? "Showing " + parts.join(" · ") : "";
}

/** The periods the picker offers. Anything longer than the deployment keeps is
    shown but refused, with the reason — quieter than an error after the click. */
const PERIODS: { label: string; seconds: number }[] = [
  { label: "Last hour", seconds: 3600 },
  { label: "Last 6 hours", seconds: 6 * 3600 },
  { label: "Last 24 hours", seconds: 24 * 3600 },
  { label: "Last 7 days", seconds: 7 * 24 * 3600 },
  { label: "Last 30 days", seconds: 30 * 24 * 3600 },
  { label: "Last 90 days", seconds: 90 * 24 * 3600 },
];

function keptText(seconds: number): string {
  const days = Math.round(seconds / 86400);
  if (days >= 1) return `this deployment keeps ${days} ${days === 1 ? "day" : "days"}`;
  const hours = Math.round(seconds / 3600);
  return `this deployment keeps ${hours} ${hours === 1 ? "hour" : "hours"}`;
}

function when(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function Audit() {
  const api = useApi();
  const apiRef = useRef(api);
  apiRef.current = api;

  const [page, setPage] = useState<AuditPage | null>(null);
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [cursor, setCursor] = useState(0); // unix ms; 0 means the horizon was reached
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<string>("");

  // Applied, not typed: the query goes to the server, so reloading on every
  // keystroke would be a request per character.
  const [operation, setOperation] = useState("");
  const [user, setUser] = useState("");
  const [text, setText] = useState("");
  const [period, setPeriod] = useState(24 * 3600);
  const [applied, setApplied] = useState({ operation: "", user: "", filter: "", period: 24 * 3600 });

  const load = useCallback((to: number, append: boolean, q: typeof applied) => {
    setLoading(true);
    setError("");
    apiRef.current
      .audit({
        to: to || undefined,
        from: Date.now() - q.period * 1000,
        operation: q.operation,
        user: q.user,
        filter: q.filter,
      })
      .then((p) => {
        setPage(p);
        setCursor(p.next);
        setEntries((prev) => (append ? [...prev, ...p.entries] : p.entries));
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load(0, false, { operation: "", user: "", filter: "", period: 24 * 3600 });
  }, [load]);

  const apply = (over?: Partial<typeof applied>) => {
    const q = { operation, user, filter: text, period, ...over };
    setApplied(q);
    setOpen("");
    load(0, false, q);
  };

  const empty = !loading && !error && entries.length === 0;

  return (
    <div className="page">
      <div className="page-head">
        <h1 className="page-title">Audit</h1>
        <span className="muted">{horizonText(page)}</span>
      </div>

      <div className="row" style={{ gap: 8, flexWrap: "wrap", marginBottom: 12 }}>
        <select
          className="input sm"
          value={period}
          onChange={(e) => {
            const p = Number(e.target.value);
            setPeriod(p);
            apply({ period: p });
          }}
          aria-label="Period"
        >
          {PERIODS.map((p) => {
            const beyond = !!page && page.horizon > 0 && p.seconds > page.horizon;
            return (
              <option
                key={p.seconds}
                value={p.seconds}
                disabled={beyond}
                title={beyond ? keptText(page!.horizon) : undefined}
              >
                {p.label}
                {beyond ? " — not kept" : ""}
              </option>
            );
          })}
        </select>
        <select
          className="input sm"
          value={operation}
          onChange={(e) => setOperation(e.target.value)}
          aria-label="Operation"
        >
          <option value="">All operations</option>
          {(page?.operations ?? []).map((op) => (
            <option key={op} value={op}>
              {op}
            </option>
          ))}
        </select>
        <input
          className="input sm"
          placeholder="User…"
          value={user}
          onChange={(e) => setUser(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && apply()}
        />
        <input
          className="input sm"
          placeholder="Pod, secret, release…"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && apply()}
        />
        <button className="btn" onClick={() => apply()} disabled={loading}>
          Apply
        </button>
        <span className="muted" style={{ marginLeft: "auto" }}>
          {scopeText(page)}
        </span>
      </div>

      {error && <div className="alert">{error}</div>}

      {empty && (
        <div className="card">
          <p className="muted" style={{ margin: 0 }}>
            Nothing recorded in this period. {horizonText(page)}.
          </p>
        </div>
      )}

      {entries.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Time</th>
              <th>User</th>
              <th>Operation</th>
              <th>Namespace</th>
              <th>Object</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e, i) => {
              const id = `${e.time}-${i}`;
              // Refused and failed are different things: one is the access
              // model saying no, the other is the cluster.
              const badge = !e.allowed ? "badge-err" : e.success ? "badge-ok" : "badge-warn";
              const label = !e.allowed ? "denied" : e.success ? "ok" : "failed";
              return (
                <Fragment key={id}>
                  <tr
                    className="tr-btn"
                    onClick={() => setOpen(open === id ? "" : id)}
                    title="Show the whole record"
                  >
                    <td className="muted">{when(e.time)}</td>
                    <td>{e.user || "—"}</td>
                    <td>{e.operation}</td>
                    <td>{e.namespace || <span className="muted">—</span>}</td>
                    <td>{e.target || <span className="muted">—</span>}</td>
                    <td>
                      <span className={"badge " + badge}>{label}</span>
                    </td>
                  </tr>
                  {open === id && (
                    <tr>
                      <td colSpan={6}>
                        <div className="console">
                          {e.error
                            ? e.error
                            : "No further detail was recorded for this action."}
                        </div>
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      )}

      <div className="row" style={{ marginTop: 12, gap: 10 }}>
        {cursor > 0 && (
          <button className="btn ghost" onClick={() => load(cursor, true, applied)} disabled={loading}>
            {loading ? "Loading…" : "Earlier"}
          </button>
        )}
        {cursor === 0 && entries.length > 0 && (
          <span className="muted">
            That is the end of this period.
            {page?.archiveNote ? " " + page.archiveNote : ""}
            {page?.archiveURL ? (
              <>
                {" "}
                <a className="link" href={page.archiveURL} target="_blank" rel="noreferrer">
                  Open the archive
                </a>
              </>
            ) : null}
          </span>
        )}
      </div>
    </div>
  );
}
