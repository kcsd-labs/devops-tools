import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSession } from "../session";
import { useApi } from "../useApi";
import { TimeRangePicker, type Preset } from "./TimeRangePicker";
import { PodGoneBanner } from "./PodGoneBanner";

const LEVELS = ["ALL", "ERROR", "WARN", "INFO", "DEBUG", "TRACE"] as const;
type Level = (typeof LEVELS)[number];

const SEVERITY: Record<string, number> = {
  TRACE: 0,
  DEBUG: 1,
  INFO: 2,
  WARN: 3,
  ERROR: 4,
};

const POLL_MS = 2000; // refresh interval in live mode
const LIVE_WINDOW_SEC = 300; // live mode over Loki shows a rolling five-minute window
const PAGE = 1000; // Loki page size; "load more" fetches the next one

type LokiRow = { ms: number; body: string };

/** Parses "<loki-timestamp-ms>\t<line>" into rows sorted oldest first. */
function parseLoki(text: string): LokiRow[] {
  const out: LokiRow[] = [];
  for (const l of text.split("\n")) {
    if (!l) continue;
    const tab = l.indexOf("\t");
    if (tab < 0) {
      out.push({ ms: 0, body: l });
      continue;
    }
    out.push({ ms: Number(l.slice(0, tab)), body: l.slice(tab + 1) });
  }
  out.sort((a, b) => a.ms - b.ms);
  return out;
}

const RANGES: Preset[] = [
  { v: "15m", l: "Last 15 minutes", sec: 900 },
  { v: "30m", l: "Last 30 minutes", sec: 1800 },
  { v: "1h", l: "Last hour", sec: 3600 },
  { v: "3h", l: "Last 3 hours", sec: 10800 },
  { v: "6h", l: "Last 6 hours", sec: 21600 },
  { v: "12h", l: "Last 12 hours", sec: 43200 },
  { v: "24h", l: "Last 24 hours", sec: 86400 },
  { v: "2d", l: "Last 2 days", sec: 172800 },
];

function pad(n: number, l = 2) {
  return String(n).padStart(l, "0");
}

/** Loki timestamp (epoch ms) → the viewer's local time. */
function fmtLocal(ms: number): string {
  const d = new Date(ms);
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
  );
}

function levelOf(line: string): string | null {
  const m = line.match(/\b(ERROR|WARN(?:ING)?|INFO|DEBUG|TRACE)\b/);
  if (!m) return null;
  return m[1] === "WARNING" ? "WARN" : m[1];
}

// Colours for the [pod] tags when several pods are merged into one stream.
const POD_COLORS = [
  "#4ea1ff",
  "#ffb454",
  "#a78bfa",
  "#4ade80",
  "#f472b6",
  "#22d3ee",
  "#facc15",
  "#fb7185",
  "#34d399",
  "#c084fc",
];

function podColor(tag: string): string {
  let h = 0;
  for (let i = 0; i < tag.length; i++) h = (h * 31 + tag.charCodeAt(i)) >>> 0;
  return POD_COLORS[h % POD_COLORS.length];
}

/**
 * Renders one line, colouring the [pod] tag. A leading local timestamp (added
 * in Loki mode) is left untouched.
 */
function renderLine(line: string) {
  const m = line.match(/^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}  )?\[([^\]]+)\]\s([\s\S]*)$/);
  if (!m) return line || " ";
  return (
    <>
      {m[1]}
      <span className="pod-tag" style={{ color: podColor(m[2]) }}>
        [{m[2]}]
      </span>{" "}
      {m[3]}
    </>
  );
}

function lineClass(line: string): string {
  switch (levelOf(line)) {
    case "ERROR":
      return "log-line log-error";
    case "WARN":
      return "log-line log-warn";
    case "DEBUG":
    case "TRACE":
      return "log-line log-debug";
    default:
      return "log-line";
  }
}

export function LogViewer({
  namespace,
  pod,
  pods,
  appLabel: appLabelProp,
}: {
  namespace: string;
  pod?: string;
  pods?: string[];
  /** Service label supplied by the parent; survives tab switches and remounts. */
  appLabel?: string;
}) {
  const { token } = useSession();
  const api = useApi();
  const multi = !!(pods && pods.length);
  const podsKey = pods ? pods.join(",") : "";

  const [lines, setLines] = useState<string[]>([]);
  const [lokiRaw, setLokiRaw] = useState<LokiRow[]>([]); // loaded Loki rows, oldest first
  const [hasMore, setHasMore] = useState(false); // the last page came back full
  const [loadingMore, setLoadingMore] = useState(false);
  const [containers, setContainers] = useState<string[]>([]);
  const [container, setContainer] = useState(""); // "" means the default container
  const [selfApp, setSelfApp] = useState(""); // resolved here when used standalone
  const appLabel = appLabelProp || selfApp;
  const [gone, setGone] = useState(false); // the pod was replaced (Kubernetes returned 404)
  const [source, setSource] = useState<"k8s" | "loki">("k8s");
  const [lokiEnabled, setLokiEnabled] = useState(false);
  const [range, setRange] = useState("1h");
  const [customFrom, setCustomFrom] = useState("");
  const [customTo, setCustomTo] = useState("");
  const [localTime, setLocalTime] = useState(false); // default: show the container's own timestamp
  const [scope, setScope] = useState<"pod" | "service">("service");
  const [live, setLive] = useState(false);
  const [tail, setTail] = useState(1000);
  const [filter, setFilter] = useState("");
  const [level, setLevel] = useState<Level>("ALL");
  const [order, setOrder] = useState<"asc" | "desc">("desc"); // newest first by default
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const boxRef = useRef<HTMLDivElement>(null);
  const scrollToFresh = useRef(false);

  const lokiMode = source === "loki";

  // The token and client live in refs so a silent session renewal does not
  // rebuild the fetch callbacks. Otherwise the logs would reload with a
  // spinner, the scroll would jump, and in Loki mode the pages loaded through
  // "load more" would be thrown away.
  const headersRef = useRef<Record<string, string> | undefined>(undefined);
  headersRef.current = token ? { Authorization: `Bearer ${token}` } : undefined;
  const apiRef = useRef(api);
  apiRef.current = api;

  // Only offer the Loki toggle when this deployment actually has Loki.
  useEffect(() => {
    let cancelled = false;
    apiRef.current
      .me()
      .then((m) => !cancelled && setLokiEnabled(!!m.capabilities?.loki))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  // Offer a container picker for multi-container pods, like kubectl does.
  useEffect(() => {
    if (multi || !pod) return;
    let cancelled = false;
    fetch(`/api/namespaces/${namespace}/pods/${pod}/containers`, { headers: headersRef.current })
      .then((r) => (r.ok ? r.json() : { containers: [], app: "" }))
      .then((d: { containers?: string[]; app?: string }) => {
        if (cancelled) return;
        setContainers(Array.isArray(d.containers) ? d.containers : []);
        setSelfApp(d.app || "");
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [namespace, pod, multi]);

  /** [from, to] in unix milliseconds for a Loki query. */
  const rangeBounds = useCallback((): [number, number] => {
    const to = Date.now();
    if (live) return [to - LIVE_WINDOW_SEC * 1000, to];
    if (range === "custom") {
      const from = customFrom ? new Date(customFrom).getTime() : to - 3600_000;
      const until = customTo ? new Date(customTo).getTime() : to;
      return [from, until];
    }
    const sec = RANGES.find((r) => r.v === range)?.sec ?? 3600;
    return [to - sec * 1000, to];
  }, [live, range, customFrom, customTo]);

  /** Tail of the live Kubernetes logs — used for a single pod and for merged pods. */
  const fetchKube = useCallback(
    async (showLoader: boolean) => {
      if (showLoader) setLoading(true);
      try {
        let text: string;
        if (multi) {
          const res = await fetch(
            `/api/namespaces/${namespace}/logs?pods=${encodeURIComponent(podsKey)}&tail=${tail}`,
            { headers: headersRef.current }
          );
          if (!res.ok) throw new Error(`Request failed (${res.status})`);
          text = await res.text();
        } else {
          const url =
            `/api/namespaces/${namespace}/pods/${pod}/logs?tail=${tail}` +
            (container ? `&container=${encodeURIComponent(container)}` : "");
          const res = await fetch(url, { headers: headersRef.current });
          if (res.status === 404) {
            // The pod was replaced by a deployment: explain that instead of
            // showing a raw error.
            setGone(true);
            setLive(false);
            setError("");
            return;
          }
          if (!res.ok) throw new Error(`Request failed (${res.status})`);
          text = await res.text();
        }
        setGone(false);
        setLines(text.split("\n"));
        setError("");
      } catch (e: any) {
        setError(e.message);
      } finally {
        if (showLoader) setLoading(false);
      }
    },
    [multi, namespace, pod, container, podsKey, tail]
  );

  /** Loki: newest page within the selected range. Replaces what is on screen. */
  const fetchLokiFresh = useCallback(
    async (showLoader: boolean) => {
      if (showLoader) setLoading(true);
      try {
        const [from, to] = rangeBounds();
        const wholeService = scope === "service";
        const selector = multi
          ? { pods: podsKey.split(","), ...(wholeService ? { scope: "service" as const } : {}) }
          : wholeService
            ? appLabel
              ? { app: appLabel } // knowing the service works even for a deleted pod
              : { pod, scope: "service" as const, container }
            : { pod, container };
        const text = await apiRef.current.lokiLogs(namespace, { ...selector, from, to, limit: PAGE });
        const rows = parseLoki(text);
        setLokiRaw(rows);
        setHasMore(!live && rows.length >= PAGE);
        setError("");
      } catch (e: any) {
        setError(e.message);
      } finally {
        if (showLoader) setLoading(false);
      }
    },
    [rangeBounds, live, namespace, multi, podsKey, pod, container, scope, appLabel]
  );

  /** Loki: fetch the next, older page, using the oldest loaded row as cursor. */
  const loadMore = useCallback(async () => {
    if (!lokiRaw.length || loadingMore) return;
    setLoadingMore(true);
    try {
      const [from] = rangeBounds();
      const cursor = lokiRaw[0].ms;
      const wholeService = scope === "service";
      const selector = multi
        ? { pods: podsKey.split(","), ...(wholeService ? { scope: "service" as const } : {}) }
        : wholeService
          ? appLabel
            ? { app: appLabel }
            : { pod, scope: "service" as const, container }
          : { pod, container };
      const text = await apiRef.current.lokiLogs(namespace, {
        ...selector,
        from,
        to: cursor,
        limit: PAGE,
      });
      const older = parseLoki(text).filter((r) => r.ms < cursor); // strictly older than the cursor
      if (older.length) setLokiRaw((prev) => [...older, ...prev]);
      setHasMore(older.length >= PAGE);
      setError("");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setLoadingMore(false);
    }
  }, [lokiRaw, loadingMore, rangeBounds, namespace, multi, podsKey, pod, container, scope, appLabel]);

  /** After a pod is replaced, its history is still in Loki under the service label. */
  const switchToLoki = useCallback(() => {
    setSource("loki");
    setScope("service");
    setGone(false);
  }, []);

  // Loki rows become display lines, optionally prefixed with the local time the
  // line was ingested (the application's own timestamp stays inside the line).
  useEffect(() => {
    if (!lokiMode) return;
    setLines(lokiRaw.map((r) => (localTime && r.ms ? `${fmtLocal(r.ms)}  ${r.body}` : r.body)));
  }, [lokiMode, lokiRaw, localTime]);

  // Live polls; otherwise it is a single snapshot that scrolls to the newest line.
  useEffect(() => {
    const fresh = lokiMode ? fetchLokiFresh : fetchKube;
    if (!live) {
      scrollToFresh.current = true;
      void fresh(true);
      return;
    }
    void fresh(false);
    const id = setInterval(() => void fresh(false), POLL_MS);
    return () => clearInterval(id);
  }, [live, lokiMode, fetchLokiFresh, fetchKube]);

  // Scroll to the newest line: always in live mode, once after a snapshot.
  // Newest-first means scrolling up, oldest-first means scrolling down.
  useEffect(() => {
    if (!boxRef.current) return;
    if (live || scrollToFresh.current) {
      boxRef.current.scrollTop = order === "desc" ? 0 : boxRef.current.scrollHeight;
      scrollToFresh.current = false;
    }
  }, [lines, live, order]);

  const download = useCallback(() => {
    const blob = new Blob([lines.join("\n")], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    const ts = new Date().toISOString().replace(/[:.]/g, "-");
    a.href = url;
    a.download = `${pod ?? namespace}-${ts}.log`;
    a.click();
    URL.revokeObjectURL(url);
  }, [lines, pod, namespace]);

  const visible = useMemo(() => {
    const f = filter.toLowerCase();
    const out = lines.filter((l) => {
      if (f && !l.toLowerCase().includes(f)) return false;
      if (level !== "ALL") {
        const lv = levelOf(l);
        if (!lv || SEVERITY[lv] < SEVERITY[level]) return false;
      }
      return true;
    });
    return order === "desc" ? out.reverse() : out;
  }, [lines, filter, level, order]);

  // Paging is only meaningful on an unfiltered Loki snapshot: with a filter
  // applied a page may come back empty and look like the end of the logs.
  const showLoadMore = lokiMode && hasMore && !live && !filter && level === "ALL";
  const loadMoreBtn = (
    <button className="btn ghost load-more" onClick={loadMore} disabled={loadingMore}>
      {loadingMore ? "Loading…" : `↓ Load ${PAGE} older lines`}
    </button>
  );

  return (
    <div>
      <div className="logbar">
        <button
          className={"btn sm " + (live ? "danger" : "")}
          onClick={() => setLive((v) => !v)}
          title="Follow the log tail, refreshing every two seconds"
        >
          {live ? "■ Stop" : "● Live"}
        </button>

        {lokiEnabled && (
          <div className="seg" title="Log source">
            <button
              className={"seg-btn " + (source === "k8s" ? "on" : "")}
              onClick={() => setSource("k8s")}
            >
              Live
            </button>
            <button
              className={"seg-btn " + (source === "loki" ? "on" : "")}
              onClick={() => setSource("loki")}
            >
              History
            </button>
          </div>
        )}

        {!multi && containers.length > 1 && (
          <select
            className="input sm select"
            value={container}
            onChange={(e) => setContainer(e.target.value)}
            title="Container"
          >
            {containers.map((c, i) => (
              <option key={c} value={i === 0 ? "" : c}>
                {c}
                {i === 0 ? " (default)" : ""}
              </option>
            ))}
          </select>
        )}

        {lokiMode && !live && (
          <TimeRangePicker
            presets={RANGES}
            range={range}
            customFrom={customFrom}
            customTo={customTo}
            onApply={(r, f, t) => {
              setRange(r);
              if (r === "custom") {
                setCustomFrom(f);
                setCustomTo(t);
              }
              scrollToFresh.current = true;
            }}
          />
        )}

        {lokiMode && (
          <select
            className="input sm select"
            value={scope}
            onChange={(e) => setScope(e.target.value as "pod" | "service")}
            title="How much to search"
          >
            <option value="pod">{multi ? "These pods" : "This pod"}</option>
            <option value="service">
              {multi ? "These services (incl. deleted pods)" : "Whole service (incl. deleted pods)"}
            </option>
          </select>
        )}

        {lokiMode && (
          <select
            className="input sm select"
            value={localTime ? "local" : "container"}
            onChange={(e) => setLocalTime(e.target.value === "local")}
            title="Which timestamp to show at the start of each line"
          >
            <option value="local">Local time</option>
            <option value="container">Container time</option>
          </select>
        )}

        <input
          className="input sm"
          placeholder="Filter text…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />

        <select
          className="input sm select"
          value={level}
          onChange={(e) => setLevel(e.target.value as Level)}
        >
          {LEVELS.map((l) => (
            <option key={l} value={l}>
              {l === "ALL" ? "All levels" : l}
            </option>
          ))}
        </select>

        <select
          className="input sm select"
          value={order}
          onChange={(e) => setOrder(e.target.value as "asc" | "desc")}
          title="Line order"
        >
          <option value="asc">Oldest first</option>
          <option value="desc">Newest first</option>
        </select>

        {!lokiMode && (
          <label className="muted tail-label">
            tail
            <input
              className="input sm tail-input"
              type="number"
              value={tail}
              onChange={(e) => setTail(Number(e.target.value) || 100)}
            />
          </label>
        )}

        {!live && (
          <button
            className="btn ghost sm"
            onClick={() => {
              scrollToFresh.current = true;
              void (lokiMode ? fetchLokiFresh(true) : fetchKube(true));
            }}
          >
            Refresh
          </button>
        )}

        <button
          className="btn ghost sm"
          onClick={download}
          disabled={!lines.length}
          title="Download the loaded lines as a file"
        >
          ⭳ Download
        </button>

        <span className="muted count">
          {visible.length} / {lines.length}
        </span>
      </div>

      {error && <div className="alert">{error}</div>}

      {gone && (
        <PodGoneBanner
          namespace={namespace}
          appLabel={appLabel}
          secondaryLabel={lokiEnabled && appLabel ? "Show history" : undefined}
          onSecondary={lokiEnabled && appLabel ? switchToLoki : undefined}
        />
      )}

      <div className="card no-pad">
        <div className="console" ref={boxRef}>
          {/* Oldest first: older lines are loaded at the top. */}
          {showLoadMore && order === "asc" && loadMoreBtn}
          {loading ? (
            "Loading…"
          ) : visible.length === 0 ? (
            "— nothing to show —"
          ) : (
            visible.map((l, i) => (
              <div key={i} className={lineClass(l)}>
                {renderLine(l)}
              </div>
            ))
          )}
          {/* Newest first: older lines are loaded at the bottom, as in Grafana. */}
          {showLoadMore && order === "desc" && loadMoreBtn}
        </div>
      </div>
    </div>
  );
}
