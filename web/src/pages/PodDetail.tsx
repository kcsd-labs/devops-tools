import { useEffect, useRef, useState } from "react";
import { useParams, Link } from "react-router-dom";
import { useApi } from "../useApi";
import { LogViewer } from "../components/LogViewer";
import { PodMetrics } from "../components/PodMetrics";
import { PodGoneBanner } from "../components/PodGoneBanner";

type Tab = "describe" | "logs" | "metrics";

export function PodDetail() {
  const { namespace = "", pod = "" } = useParams();
  const api = useApi();
  const [tab, setTab] = useState<Tab>("describe");
  const [describe, setDescribe] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [appLabel, setAppLabel] = useState("");
  const [gone, setGone] = useState(false);

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
      .then((d) => !cancelled && setAppLabel(d.app || ""))
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
    <div className="page">
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
      {tab === "metrics" && !gone && (
        <PodMetrics namespace={namespace} pod={pod} onGone={() => setGone(true)} />
      )}
    </div>
  );
}
