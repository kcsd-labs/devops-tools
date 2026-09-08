import { useCallback, useEffect, useRef, useState } from "react";
import { useApi } from "../useApi";
import type { PodMetrics as PodMetricsData } from "../api";
import { Chart } from "./Chart";

// These must match the ranges the backend understands, otherwise an unknown
// value silently falls back to one hour.
const RANGES = [
  { v: "15m", l: "15m" },
  { v: "30m", l: "30m" },
  { v: "1h", l: "1h" },
  { v: "1h30m", l: "1h 30m" },
];
const POLL_MS = 30000;

const fmtCpu = (v: number) => (v >= 1 ? v.toFixed(2) : `${Math.round(v * 1000)}m`);
const fmtMem = (v: number) =>
  v >= 1024 ** 3 ? `${(v / 1024 ** 3).toFixed(2)} Gi` : `${(v / 1024 ** 2).toFixed(0)} Mi`;
const fmtGc = (v: number) => `${(v * 1000).toFixed(1)} ms/s`;

export function PodMetrics({
  namespace,
  pod,
  onGone,
}: {
  namespace: string;
  pod: string;
  onGone?: () => void;
}) {
  const api = useApi();
  const [range, setRange] = useState("1h");
  const [data, setData] = useState<PodMetricsData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  // Both the client and the parent's inline callback go into refs: otherwise a
  // renewed token or a re-render would rebuild `load` and the charts would
  // flash a spinner every few minutes.
  const apiRef = useRef(api);
  apiRef.current = api;
  const onGoneRef = useRef(onGone);
  onGoneRef.current = onGone;

  const load = useCallback(
    (showLoader: boolean) => {
      if (showLoader) setLoading(true);
      apiRef.current
        .podMetrics(namespace, pod, range)
        .then((d) => {
          setData(d);
          setError("");
        })
        .catch((e: any) => {
          if (e?.status === 404) onGoneRef.current?.(); // the pod is gone — parent shows the banner
          else setError(e.message);
        })
        .finally(() => showLoader && setLoading(false));
    },
    [namespace, pod, range]
  );

  useEffect(() => {
    load(true);
    const id = setInterval(() => load(false), POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  return (
    <div>
      <div className="metrics-bar">
        <div className="tabs-inline">
          {RANGES.map((r) => (
            <button
              key={r.v}
              className={"chip" + (range === r.v ? " active" : "")}
              onClick={() => setRange(r.v)}
            >
              {r.l}
            </button>
          ))}
        </div>
        <button className="btn ghost sm" onClick={() => load(true)}>
          Refresh
        </button>
      </div>

      {error && <div className="alert">{error}</div>}

      {loading ? (
        <div className="muted center pad">Loading metrics…</div>
      ) : data ? (
        <div className="chart-grid">
          <Chart
            title="CPU (cores)"
            points={data.cpu.usage}
            color="#4ea1ff"
            limit={data.cpu.limit}
            format={fmtCpu}
          />
          <Chart
            title="Memory"
            points={data.memory.usage}
            color="#34d399"
            limit={data.memory.limit}
            format={fmtMem}
          />
          {data.jvm && (
            <>
              <Chart title="JVM heap" points={data.jvm.heap} color="#a78bfa" format={fmtMem} />
              <Chart title="JVM non-heap" points={data.jvm.nonheap} color="#22d3ee" format={fmtMem} />
              <Chart title="GC pause" points={data.jvm.gc} color="#ffb454" format={fmtGc} />
            </>
          )}
        </div>
      ) : (
        <div className="muted center pad">No data</div>
      )}

      {data && !data.jvm && (
        <p className="muted metrics-note">
          No JVM metrics for this pod — it either is not a JVM workload or does not expose them to
          Prometheus.
        </p>
      )}
    </div>
  );
}
