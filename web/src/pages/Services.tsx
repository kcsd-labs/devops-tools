import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useApi } from "../useApi";
import type { ServiceInfo, ServiceList, ServicePort } from "../api";

const FULL_NAMES_KEY = "devops-tools.servicesFullNames";
const NAMESPACE_KEY = "devops-tools.servicesNamespace";

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
function portText(p: ServicePort): { label: string; target: string; rest: string } {
  const target = p.targetPort && p.targetPort !== String(p.port) ? p.targetPort : "";
  let rest = "";
  if (p.protocol && p.protocol !== "TCP") rest += ` ${p.protocol}`;
  if (p.nodePort) rest += ` · node ${p.nodePort}`;
  return { label: p.name ?? "", target, rest };
}

/** Calls to it will fail, or some of them will. Unknown counts are not a
    problem: the service account may simply not read EndpointSlices. */
function notReady(s: ServiceInfo): boolean {
  const e = s.endpoints;
  if (!e || s.type === "ExternalName") return false;
  if (e.total === 0) return s.hasSelector;
  return e.ready < e.total;
}

/** Something on the row needs looking at: pods that are not ready, or a port
    the pods do not declare. */
function problem(s: ServiceInfo): boolean {
  return notReady(s) || s.ports.some((p) => p.declared);
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

/** The namespace this browser last looked at, so coming back lands there. */
function rememberedNamespace(): string {
  try {
    return localStorage.getItem(NAMESPACE_KEY) ?? "";
  } catch {
    return "";
  }
}

export function Services() {
  const api = useApi();
  const [params, setParams] = useSearchParams();
  const [namespaces, setNamespaces] = useState<string[] | null>(null);
  const [data, setData] = useState<ServiceList | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [onlyProblems, setOnlyProblems] = useState(false);
  const [full, setFull] = useState<boolean>(() => {
    try {
      return localStorage.getItem(FULL_NAMES_KEY) === "1";
    } catch {
      return false;
    }
  });
  const [copied, setCopied] = useState("");
  const timer = useRef<number>();

  // The namespace is in the address, so a link to a namespace's services can
  // be passed around; failing that, the one this browser looked at last;
  // failing that, the first.
  const asked = params.get("namespace") ?? "";
  const namespace =
    namespaces && namespaces.includes(asked)
      ? asked
      : namespaces?.includes(rememberedNamespace())
        ? rememberedNamespace()
        : (namespaces?.[0] ?? "");

  useEffect(() => {
    api
      .serviceNamespaces()
      .then((r) => setNamespaces(r.namespaces))
      .catch((e) => setError(String(e?.message ?? e)));
  }, [api]);

  useEffect(() => {
    if (!namespace) return;
    try {
      localStorage.setItem(NAMESPACE_KEY, namespace);
    } catch {
      // A remembered preference, nothing more.
    }
    let live = true;
    setLoading(true);
    setError("");
    api
      .services(namespace)
      .then((d) => live && setData(d))
      .catch((e) => {
        if (!live) return;
        setData(null);
        setError(String(e?.message ?? e));
      })
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [api, namespace]);

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

  // Everything but the problems switch, so its count says how many it would
  // leave rather than how many there are.
  const matching = useMemo(() => {
    if (!data) return [];
    const q = query.trim().toLowerCase();
    return data.services.filter(
      (s) =>
        !q ||
        s.name.toLowerCase().includes(q) ||
        s.ports.some((p) => String(p.port) === q || (p.name ?? "").toLowerCase().includes(q))
    );
  }, [data, query]);

  const problemCount = useMemo(() => matching.filter(problem).length, [matching]);
  const shown = onlyProblems ? matching.filter(problem) : matching;

  const domain = data?.clusterDomain || "cluster.local";
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">Services</h1>
          {data?.mayIngress && (
            <div className="seg-switch" role="tablist">
              <span className="on">Services</span>
              <Link to={`/ingresses${namespace ? "?namespace=" + encodeURIComponent(namespace) : ""}`}>Ingress</Link>
            </div>
          )}
          <div className="muted svc-sub">
            How to reach each service from another pod
            {data && !loading && ` · ${plural(data.services.length, "service")}`}
          </div>
        </div>
      </div>

      <div className="row svc-toolbar">
        {namespaces && namespaces.length > 1 && (
          <select
            className="input"
            style={{ flex: "0 0 auto", width: 260 }}
            value={namespace}
            onChange={(e) => {
              setQuery("");
              setParams({ namespace: e.target.value }, { replace: true });
            }}
            aria-label="Namespace"
          >
            {namespaces.map((ns) => (
              <option key={ns} value={ns}>
                {ns}
              </option>
            ))}
          </select>
        )}
        {namespaces && namespaces.length === 1 && (
          <span className="svc-only-ns">{namespace}</span>
        )}
        <input
          className="input"
          style={{ minWidth: 220 }}
          placeholder="Find a service or port…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button
          className={"svc-toggle" + (onlyProblems ? " on" : "")}
          onClick={() => setOnlyProblems((v) => !v)}
          title="Only services with pods that are not ready, or a port the pods do not declare"
        >
          Problems only <b className={problemCount ? "warn" : ""}>{problemCount}</b>
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

      {(!namespaces || loading) && !error && <div className="muted">Loading…</div>}

      {data && !loading && shown.length === 0 && (
        <div className="card">
          <p className="muted" style={{ margin: 0 }}>
            {data.services.length === 0
              ? `There are no services in ${namespace}.`
              : onlyProblems && matching.length > 0
                ? "Nothing here needs looking at."
                : "No service matches."}
          </p>
        </div>
      )}

      {data && !loading && shown.length > 0 && (
        <div className="card no-pad">
          <table className="table svc-table">
            <thead>
              <tr>
                <th style={{ width: "24%" }}>Service</th>
                <th>Address</th>
                <th style={{ width: "16%" }}>Port → pod port</th>
                <th style={{ width: "7%" }}>Pods</th>
                <th style={{ width: "1%" }} aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {shown.map((s) => {
                const external = s.type === "ExternalName";
                // A service with no ports still has a name to call; headless
                // ones often do.
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
                      {s.release && (
                        <div className="svc-owner" title={s.chart}>
                          helm{" "}
                          {data.mayHelm ? (
                            <Link className="link" to={`/ns/${s.namespace}/helm`}>
                              {s.release}
                            </Link>
                          ) : (
                            <b>{s.release}</b>
                          )}
                        </div>
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
                      {s.ports
                        .filter((p) => p.declared)
                        .map((p) => (
                          <div className="svc-note warn" key={"declared/" + p.port}>
                            ⚠ The pods declare {p.declared!.join(", ")}, not {p.targetPort} — calls
                            are likely to reach nothing.
                          </div>
                        ))}
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
                              {p.port}
                              {t.target && (
                                <>
                                  {" → "}
                                  <span className={p.declared ? "warn-text" : ""}>{t.target}</span>
                                </>
                              )}
                              {!t.target && p.declared && <span className="warn-text"> ⚠</span>}
                              {t.rest}
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
                    <td>
                      <div className="svc-acts">
                        {data.mayRequest && !external && (
                          <Link
                            className="btn ghost sm svc-act svc-act-main"
                            to={`/ns/${s.namespace}/services/${s.name}`}
                            title="Send requests to it, and keep the useful ones"
                          >
                            Requests ›
                          </Link>
                        )}
                        {data.mayLogs && (s.pods?.length ?? 0) > 0 && (
                          <Link
                            className="btn ghost sm svc-act"
                            to={`/ns/${s.namespace}/logs?pods=${s.pods!.join(",")}`}
                            title="Merged logs of the pods behind it"
                          >
                            Logs
                          </Link>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
