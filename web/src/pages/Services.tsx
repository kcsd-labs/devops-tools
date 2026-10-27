import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useApi } from "../useApi";
import type { ServiceInfo, ServiceList, ServicePort } from "../api";

const FULL_NAMES_KEY = "devops-tools.servicesFullNames";

/** The scheme a port speaks, where it says so: in its appProtocol, or in its
    name — "http", "http-metrics", "https" — by the convention most charts
    follow. A port that does not say gets no scheme rather than a guess: gRPC,
    databases and brokers take a bare host and port, and a wrong http:// in a
    copied address is worse than none. */
function scheme(p?: ServicePort): string {
  if (!p) return "";
  const hint = (p.appProtocol || p.name || "").toLowerCase();
  if (hint.startsWith("https") || (!hint && p.port === 443)) return "https://";
  if (hint.startsWith("http") || hint.endsWith("/h2c")) return "http://";
  return "";
}

/** The address another pod calls. Short by default: the namespace is enough
    from anywhere in the cluster, and the full name is noise in a config file.
    Returned in parts so the page can quieten the namespace — the name and the
    port are what a reader is looking for.

    The scheme is not shown, since the port column already says what the port
    speaks, but it is in what the copy button puts on the clipboard. */
function addressParts(s: ServiceInfo, full: boolean, domain: string, p?: ServicePort) {
  return {
    scheme: scheme(p),
    name: s.name,
    rest: "." + s.namespace + (full ? `.svc.${domain}` : ""),
    port: p ? `:${p.port}` : "",
  };
}

function addressText(s: ServiceInfo, full: boolean, domain: string, p?: ServicePort): string {
  const a = addressParts(s, full, domain, p);
  return a.scheme + a.name + a.rest + a.port;
}

/** The port called and the port the pod listens on, side by side: the two
    differing — and one of them being wrong — is the commonest reason a call
    goes nowhere. */
function portText(p: ServicePort): { label: string; ports: string } {
  let ports =
    p.targetPort && p.targetPort !== String(p.port) ? `${p.port} → ${p.targetPort}` : `${p.port}`;
  if (p.protocol && p.protocol !== "TCP") ports += ` ${p.protocol}`;
  if (p.nodePort) ports += ` · node ${p.nodePort}`;
  return { label: p.name ?? "", ports };
}

/** Calls to it will fail, or some of them will. Unknown counts are not a
    problem: the service account may simply not read EndpointSlices. */
function notReady(s: ServiceInfo): boolean {
  const e = s.endpoints;
  if (!e || s.type === "ExternalName") return false;
  if (e.total === 0) return s.hasSelector;
  return e.ready < e.total;
}

/** What a row needs said under its address, if anything. */
function note(s: ServiceInfo): { text: string; warn: boolean; pods: boolean } | null {
  if (s.type === "ExternalName") {
    return {
      text: `alias for ${s.externalName} — answers outside the cluster`,
      warn: false,
      pods: false,
    };
  }
  const e = s.endpoints;
  if (e && e.total === 0 && s.hasSelector) {
    return { text: "Its selector matches no pod — calls will fail.", warn: true, pods: true };
  }
  if (e && e.total === 0) {
    return {
      text: "No selector: whoever created it manages its endpoints, and there are none.",
      warn: true,
      pods: false,
    };
  }
  if (e && e.ready === 0) {
    return { text: "No pod behind it is ready — calls will fail.", warn: true, pods: true };
  }
  if (e && e.ready < e.total) {
    return { text: "Some pods behind it are not ready.", warn: true, pods: true };
  }
  return null;
}

/** Copies through the Clipboard API where the page is a secure context, and
    through a hidden field where it is not — a portal served over plain http
    inside a network is common, and the API is simply absent there. */
async function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const field = document.createElement("textarea");
  field.value = text;
  field.setAttribute("readonly", "");
  field.style.position = "fixed";
  field.style.opacity = "0";
  document.body.appendChild(field);
  field.select();
  const ok = document.execCommand("copy");
  document.body.removeChild(field);
  if (!ok) throw new Error("copy refused");
}

export function Services() {
  const api = useApi();
  const [data, setData] = useState<ServiceList | null>(null);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [namespace, setNamespace] = useState("");
  const [onlyNotReady, setOnlyNotReady] = useState(false);
  const [full, setFull] = useState<boolean>(() => {
    try {
      return localStorage.getItem(FULL_NAMES_KEY) === "1";
    } catch {
      return false;
    }
  });
  const [copied, setCopied] = useState("");
  const timer = useRef<number>();

  useEffect(() => {
    api
      .services()
      .then(setData)
      .catch((e) => setError(String(e?.message ?? e)));
  }, [api]);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  useEffect(() => {
    try {
      localStorage.setItem(FULL_NAMES_KEY, full ? "1" : "0");
    } catch {
      // A remembered preference, nothing more.
    }
  }, [full]);

  const copy = async (text: string) => {
    try {
      await copyText(text);
      setCopied(text);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopied(""), 1500);
    } catch {
      setError("The browser refused to copy. Select the address and copy it by hand.");
    }
  };

  // Everything but the not-ready switch, so its count says how many it would
  // leave rather than how many there are anywhere.
  const matching = useMemo(() => {
    if (!data) return [];
    const q = query.trim().toLowerCase();
    return data.services.filter(
      (s) =>
        (!namespace || s.namespace === namespace) &&
        (!q ||
          s.name.toLowerCase().includes(q) ||
          s.namespace.toLowerCase().includes(q) ||
          s.ports.some((p) => String(p.port) === q || (p.name ?? "").toLowerCase().includes(q)))
    );
  }, [data, query, namespace]);

  const notReadyCount = useMemo(() => matching.filter(notReady).length, [matching]);
  const shown = onlyNotReady ? matching.filter(notReady) : matching;

  // Grouped by namespace, which the backend has already sorted by.
  const groups = useMemo(() => {
    const out: { namespace: string; services: ServiceInfo[] }[] = [];
    for (const s of shown) {
      const last = out[out.length - 1];
      if (last && last.namespace === s.namespace) last.services.push(s);
      else out.push({ namespace: s.namespace, services: [s] });
    }
    return out;
  }, [shown]);

  const domain = data?.clusterDomain || "cluster.local";
  const many = (data?.namespaces.length ?? 0) > 1;
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">Services</h1>
          <div className="muted svc-sub">
            How to reach each service from another pod
            {data &&
              ` · ${
                data.namespaces.length <= 3
                  ? data.namespaces.join(", ")
                  : plural(data.namespaces.length, "namespace")
              } · ${plural(data.services.length, "service")}`}
          </div>
        </div>
      </div>

      <div className="row svc-toolbar">
        <input
          className="input"
          style={{ minWidth: 220 }}
          placeholder="Find a service, namespace or port…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {many && (
          <select
            className="input"
            style={{ flex: "0 0 auto", width: 240 }}
            value={namespace}
            onChange={(e) => setNamespace(e.target.value)}
            aria-label="Namespace"
          >
            <option value="">All my namespaces</option>
            {data!.namespaces.map((ns) => (
              <option key={ns} value={ns}>
                {ns}
              </option>
            ))}
          </select>
        )}
        <button
          className={"svc-toggle" + (onlyNotReady ? " on" : "")}
          onClick={() => setOnlyNotReady((v) => !v)}
          title="Only services that have pods behind them which are not ready"
        >
          Not ready only <b className={notReadyCount ? "warn" : ""}>{notReadyCount}</b>
        </button>
        <div className="svc-seg" role="group" aria-label="Address form">
          <button className={full ? "" : "on"} onClick={() => setFull(false)}>
            name.namespace
          </button>
          <button className={full ? "on" : ""} onClick={() => setFull(true)}>
            full name
          </button>
        </div>
      </div>

      {error && <div className="alert">{error}</div>}

      {data && data.unreadable.length > 0 && (
        <div className="alert svc-partial">
          Could not read {data.unreadable.join(", ")} — the services there are not in this list.
        </div>
      )}

      {!data && !error && <div className="muted">Loading…</div>}

      {data && shown.length === 0 && (
        <div className="card">
          <p className="muted" style={{ margin: 0 }}>
            {data.services.length === 0
              ? "There are no services in these namespaces."
              : onlyNotReady && matching.length > 0
                ? "Every service here has its pods ready."
                : "No service matches."}
          </p>
        </div>
      )}

      {shown.length > 0 && (
        <div className="card no-pad">
          <table className="table svc-table">
            <thead>
              <tr>
                <th style={{ width: "24%" }}>Service</th>
                <th>Address</th>
                <th style={{ width: "20%" }}>Port → pod port</th>
                <th style={{ width: "10%" }}>Pods</th>
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <Fragment key={g.namespace}>
                  <tr className="svc-group">
                    <td colSpan={4}>
                      {g.namespace} <span>· {plural(g.services.length, "service")}</span>
                    </td>
                  </tr>
                  {g.services.map((s) => {
                    const external = s.type === "ExternalName";
                    // A service with no ports still has a name to call;
                    // headless ones often do.
                    const ports: (ServicePort | undefined)[] =
                      external || s.ports.length === 0 ? [undefined] : s.ports;
                    const n = note(s);
                    const e = s.endpoints;
                    return (
                      <tr key={s.namespace + "/" + s.name}>
                        <td>
                          <span className="svc-name">{s.name}</span>
                          {s.headless && <span className="svc-tag kind">headless</span>}
                          {external && <span className="svc-tag ext">external</span>}
                          {(s.type === "NodePort" || s.type === "LoadBalancer") && (
                            <span className="svc-tag kind">{s.type}</span>
                          )}
                        </td>
                        <td>
                          {ports.map((p) => {
                            const a = addressParts(s, full, domain, p);
                            const text = addressText(s, full, domain, p);
                            return (
                              <div className="svc-addr" key={text}>
                                <span
                                  className="mono"
                                  title={addressText(s, !full, domain, p).replace(a.scheme, "")}
                                >
                                  {a.name}
                                  <span className="svc-ns">{a.rest}</span>
                                  {a.port}
                                </span>
                                <button
                                  className={"svc-copy" + (copied === text ? " done" : "")}
                                  onClick={() => copy(text)}
                                  title={"Copy " + text}
                                >
                                  {copied === text ? "✓ copied" : "copy"}
                                </button>
                              </div>
                            );
                          })}
                          {n && (
                            <div className={"svc-note" + (n.warn ? " warn" : "")}>
                              {n.text}{" "}
                              {n.pods && (
                                <Link className="link" to={`/ns/${s.namespace}/pods`}>
                                  Pods ›
                                </Link>
                              )}
                            </div>
                          )}
                        </td>
                        <td>
                          {external || s.ports.length === 0 ? (
                            <div className="mono svc-port">—</div>
                          ) : (
                            s.ports.map((p) => {
                              const t = portText(p);
                              return (
                                <div className="mono svc-port" key={p.port + "/" + p.protocol}>
                                  {t.label && <span className="svc-port-name">{t.label} </span>}
                                  {t.ports}
                                </div>
                              );
                            })
                          )}
                        </td>
                        <td>
                          {external ? (
                            <span className="svc-pods muted">—</span>
                          ) : !e ? (
                            <span className="svc-pods muted" title="Could not read the pods behind it">
                              ?
                            </span>
                          ) : (
                            <span className={"svc-pods" + (notReady(s) ? " warn" : "")}>
                              <span className={"svc-dot" + (notReady(s) ? " warn" : "")} />
                              {e.ready} / {e.total}
                            </span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
