import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { useParams, Link } from "react-router-dom";
import { useApi } from "../useApi";
import { LogViewer } from "../components/LogViewer";
import { PodMetrics } from "../components/PodMetrics";
import { PodGoneBanner } from "../components/PodGoneBanner";

// The terminal brings xterm.js with it; loaded only when the tab is opened.
const PodTerminal = lazy(() => import("../components/PodTerminal"));

type Tab = "describe" | "logs" | "metrics" | "terminal";

export function PodDetail() {
  const { namespace = "", pod = "" } = useParams();
  const api = useApi();
  const [tab, setTab] = useState<Tab>("describe");
  const [describe, setDescribe] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [appLabel, setAppLabel] = useState("");
  const [gone, setGone] = useState(false);
  const [containers, setContainers] = useState<string[]>([]);
  const [mayExec, setMayExec] = useState(false);

  // A renewed token must not re-fetch the description and flash the page.
  const apiRef = useRef(api);
  apiRef.current = api;

  // Capture the service label while the pod still exists: once it is replaced
  // this is the only way to find its successor.
  useEffect(() => {
    setGone(false);
    let cancelled = false;
    apiRef.current
      .podContainers(namespace, pod)
      .then((d) => {
        if (cancelled) return;
        setAppLabel(d.app || "");
        setContainers(d.containers ?? []);
        setMayExec(!!d.mayExec);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [namespace, pod]);

  useEffect(() => {
    if (tab !== "describe") return;
    setError("");
    setLoading(true);
    apiRef.current
      .podDescribe(namespace, pod)
      .then((d) => {
        setDescribe(d);
        setGone(false);
      })
      .catch((e: any) => {
        if (e?.status === 404) setGone(true); // replaced — show the banner, not an error
        else setError(e.message);
      })
      .finally(() => setLoading(false));
  }, [namespace, pod, tab]);

  return (
    // The terminal fills the working area, so its prompt is never below the fold.
    <div className={"page" + (tab === "terminal" ? " fill-page" : "")}>
      <div className="page-head">
        <div>
          <Link to={`/ns/${namespace}/pods`} className="crumb">
            ← {namespace}
          </Link>
          <h1 className="page-title mono">{pod}</h1>
        </div>
      </div>

      <div className="tabs">
        <button
          className={"tab" + (tab === "describe" ? " active" : "")}
          onClick={() => setTab("describe")}
        >
          Describe
        </button>
        <button className={"tab" + (tab === "logs" ? " active" : "")} onClick={() => setTab("logs")}>
          Logs
        </button>
        <button
          className={"tab" + (tab === "metrics" ? " active" : "")}
          onClick={() => setTab("metrics")}
        >
          Metrics
        </button>
        {mayExec && (
          <button
            className={"tab" + (tab === "terminal" ? " active" : "")}
            onClick={() => setTab("terminal")}
          >
            Terminal
          </button>
        )}
      </div>

      {/* Describe and Metrics show the banner here; the log viewer has its own. */}
      {gone && tab !== "logs" && (
        <PodGoneBanner
          namespace={namespace}
          appLabel={appLabel}
          secondaryLabel="Show logs"
          onSecondary={() => setTab("logs")}
        />
      )}

      {tab === "describe" && !gone && (
        <>
          {error && <div className="alert">{error}</div>}
          <div className="card no-pad">
            <pre className="console">{loading ? "Loading…" : describe || "— empty —"}</pre>
          </div>
        </>
      )}
      {tab === "logs" && <LogViewer namespace={namespace} pod={pod} appLabel={appLabel} />}
      {tab === "terminal" && !gone && (
        <Suspense fallback={<div className="muted">Loading…</div>}>
          <PodTerminal namespace={namespace} pod={pod} containers={containers} />
        </Suspense>
      )}
      {tab === "metrics" && !gone && (
        <PodMetrics namespace={namespace} pod={pod} onGone={() => setGone(true)} />
      )}
    </div>
  );
}
