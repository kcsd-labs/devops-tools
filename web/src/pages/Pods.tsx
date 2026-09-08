import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams, useNavigate, Link } from "react-router-dom";
import { useApi } from "../useApi";
import { ApiError, type PodSummary } from "../api";
import { InfoHint } from "../components/InfoHint";
import { PodEvents } from "../components/PodEvents";
import { useDialogs } from "../components/Dialogs";
import {
  startRestartWatch,
  clearRestartWatch,
  subscribeRestarts,
  getRestartStates,
  restartKey,
  reasonText,
  type RestartState,
} from "../restartWatcher";
import {
  getRebuildStates,
  subscribeRebuilds,
  watchBuild,
  type RebuildState,
} from "../rebuildWatcher";

// What the chip in a pod's row says while its image is being rebuilt.
function rebuildLabel(r: RebuildState): string {
  switch (r.phase) {
    case "running":
      return r.kind === "pipeline" ? "pipeline" : "building";
    case "restarting":
      return "restarting";
    case "waiting-for-person":
      return "needs a person";
    case "failed":
      return "build failed";
    default:
      return "rebuilt";
  }
}

function statusClass(s: string) {
  if (s === "Running") return "badge badge-ok";
  if (s === "Pending") return "badge badge-warn";
  if (s === "Succeeded") return "badge badge-muted";
  return "badge badge-err";
}

/** A pod is unhealthy when it is not Running/Succeeded, or not all containers are ready. */
function unhealthy(p: PodSummary): boolean {
  if (p.status !== "Running" && p.status !== "Succeeded") return true;
  const [ready, total] = p.ready.split("/").map(Number);
  return Number.isFinite(ready) && Number.isFinite(total) && ready < total;
}

function age(iso: string) {
  const d = (Date.now() - new Date(iso).getTime()) / 1000;
  if (d < 3600) return `${Math.floor(d / 60)}m`;
  if (d < 86400) return `${Math.floor(d / 3600)}h`;
  return `${Math.floor(d / 86400)}d`;
}

export function Pods() {
  const { namespace = "" } = useParams();
  const api = useApi();
  const nav = useNavigate();
  const [pods, setPods] = useState<PodSummary[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [sortDir, setSortDir] = useState<"asc" | "desc">("asc");
  const [search, setSearch] = useState("");
  const [selectMode, setSelectMode] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [restarts, setRestarts] = useState<RestartState[]>(() => getRestartStates(namespace));
  const [rebuilds, setRebuilds] = useState<RebuildState[]>(() => getRebuildStates(namespace));
  // Whether this person may rebuild here. False where the deployment does not
  // manage builds at all, so the button simply never appears.
  const [canRebuild, setCanRebuild] = useState(false);
  const { dialogs, confirm, alertDlg } = useDialogs();

  // Keeping the API client in a ref means a silently renewed token does not
  // invalidate the callbacks below and cause the list to reload — that showed
  // up as the page flickering every few minutes.
  const apiRef = useRef(api);
  apiRef.current = api;

  const load = useCallback(
    (silent?: unknown) => {
      // `silent === true` is the background refresh during a rollout: no
      // spinner, no clearing of the selection. The check is strict because
      // onClick handlers pass an event object.
      if (silent !== true) {
        setLoading(true);
        setSelected(new Set());
      }
      apiRef.current
        .pods(namespace)
        .then((d) => {
          setPods(d.pods);
          setCanRebuild(d.canRebuildImage);
          setError(""); // a successful refresh clears a previous transient error
        })
        .catch((e) => setError(e.message))
        .finally(() => {
          if (silent !== true) setLoading(false);
        });
    },
    [namespace]
  );

  useEffect(load, [load]);

  useEffect(() => {
    const syncRestarts = () => setRestarts(getRestartStates(namespace));
    const syncRebuilds = () => setRebuilds(getRebuildStates(namespace));
    syncRestarts();
    syncRebuilds();
    const stopRestarts = subscribeRestarts(syncRestarts);
    const stopRebuilds = subscribeRebuilds(syncRebuilds);
    return () => {
      stopRestarts();
      stopRebuilds();
    };
  }, [namespace]);

  // While a rollout is in flight the list refreshes itself, so the user can
  // watch the old pod go and the new one arrive without pressing anything.
  const rolloutActive = restarts.some((r) => r.phase === "progressing" || r.phase === "stuck");
  useEffect(() => {
    if (!rolloutActive) return;
    const id = setInterval(() => load(true), 4000);
    return () => clearInterval(id);
  }, [rolloutActive, load]);

  // A rebuild belongs to one pod by name, so no matching is needed here.
  const rebuildFor = (podName: string): RebuildState | undefined =>
    rebuilds.find((r) => r.pod === podName);

  // The image the pod cannot pull. Empty once it can, or if the pod has gone.
  const imageOf = (podName: string): string =>
    pods.find((p) => p.name === podName && p.imageUnavailable)?.unavailableImage ?? "";

  /**
   * Does this pod belong to a workload that is currently rolling out?
   * Matched on name structure — Deployment pods are <name>-<rs-hash>-<random>,
   * StatefulSet and DaemonSet pods are <name>-<ordinal|hash> — so a workload
   * called "api" does not claim the pods of "api-worker".
   */
  const rolloutFor = (podName: string): RestartState | undefined =>
    restarts.find((r) => {
      if (!podName.startsWith(r.name + "-")) return false;
      const rest = podName.slice(r.name.length + 1).split("-");
      return rest.length <= 2 && rest.every((s) => /^[a-z0-9]+$/.test(s));
    });

  const filtered = useMemo(() => {
    const f = search.toLowerCase();
    const list = [...pods].sort((a, b) =>
      sortDir === "asc" ? a.name.localeCompare(b.name) : b.name.localeCompare(a.name)
    );
    return f ? list.filter((p) => p.name.toLowerCase().includes(f)) : list;
  }, [pods, sortDir, search]);

  const restart = async (name: string) => {
    const ok = await confirm({
      title: "Restart pod",
      message:
        `Roll out a restart of the workload behind ${name}?\n\n` +
        "The current pod keeps serving traffic until the new one is ready, so there is no downtime.",
      okText: "Restart",
    });
    if (!ok) return;
    try {
      const res = await api.restartPod(namespace, name);
      startRestartWatch(namespace, res.kind, res.name);
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.status === 409) {
        void alertDlg(
          "This pod has no controller (it is a bare pod or a Job), so there is nothing to roll out."
        );
      } else {
        void alertDlg(e.message, "Error");
      }
    }
  };

  // The image is missing from the registry; run the build that produced it
  // again. The watcher takes it from here — including restarting the workload
  // once the image is back, which is the part that finishes the job.
  const rebuildImage = async (name: string) => {
    setBusy(true);
    try {
      const d = await api.rebuildImage(namespace, name);
      if (d.pipelineGone) {
        // Recovering from this means a fresh pipeline, which also deploys, so
        // it is a different permission and a decision worth reading before
        // taking. The events dialog explains it and offers it there.
        void alertDlg(
          `${d.message}\n\nOpen the pod's warning icon to go on from here.`,
          "The pipeline is gone"
        );
        return;
      }
      watchBuild(namespace, name, d.ticket!, d.jobStatus ?? "", d.jobUrl ?? "");
    } catch (e: any) {
      void alertDlg(e.message, "Could not start the build");
    } finally {
      setBusy(false);
    }
  };

  const restartSelected = async () => {
    const names = [...selected];
    if (!names.length) return;
    const ok = await confirm({
      title: "Restart selected",
      message: `Roll out a restart for the workloads behind ${names.length} pod(s)?`,
      okText: "Restart",
    });
    if (!ok) return;
    setBusy(true);
    const errors: string[] = [];
    for (const n of names) {
      try {
        const res = await api.restartPod(namespace, n);
        startRestartWatch(namespace, res.kind, res.name); // deduplicated per workload
      } catch (e: any) {
        errors.push(
          e instanceof ApiError && e.status === 409
            ? `${n}: no controller to roll out`
            : `${n}: ${e.message}`
        );
      }
    }
    setBusy(false);
    load();
    if (errors.length) void alertDlg(errors.join("\n"), "Some restarts failed");
  };

  const toggle = (name: string) =>
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });

  const allSelected = filtered.length > 0 && filtered.every((p) => selected.has(p.name));
  const toggleAll = () => setSelected(allSelected ? new Set() : new Set(filtered.map((p) => p.name)));

  return (
    <div className="page">
      {dialogs}
      <div className="page-head">
        <div>
          <Link to="/" className="crumb">
            Namespaces
          </Link>
          <h1 className="page-title">{namespace}</h1>
        </div>
        <div className="row">
          <input
            className="input sm"
            placeholder="Filter pods…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          {selectMode ? (
            <button
              className="btn ghost"
              onClick={() => {
                setSelectMode(false);
                setSelected(new Set());
              }}
            >
              Cancel
            </button>
          ) : (
            <button className="btn ghost" onClick={() => setSelectMode(true)}>
              Select multiple
            </button>
          )}          <button className="btn ghost" onClick={() => nav(`/ns/${namespace}/helm`)}>
            Helm releases
          </button>
          <button className="btn ghost" onClick={load}>
            Refresh
          </button>
        </div>
      </div>

      {error && <div className="alert">{error}</div>}

      {/* A rollout that cannot finish: say why, and make clear the service is
          still up, because the pod list alone looks alarming. */}
      {restarts
        .filter((r) => r.phase === "stuck" || r.phase === "failed")
        .map((r) => (
          <div
            key={restartKey(r.ns, r.kind, r.name)}
            className={"alert rollout-note" + (r.phase === "stuck" ? " alert-action" : "")}
          >
            <div className="rn-text">
              <b>
                {r.name}: rollout {r.phase === "failed" ? "did not finish" : "is stuck"}
              </b>
              {r.reason && (
                <>
                  {" "}
                  — {reasonText(r.reason)}
                  {r.reasonPod ? ` (${r.reasonPod})` : ""}.
                </>
              )}{" "}
              The previous pod keeps serving traffic.
              {/* The missing tag, named. Whoever reads this banner is about to
                  go looking for it, and it is already known here. */}
              {r.reason === "image" && imageOf(r.reasonPod) && (
                <div className="rn-detail mono">{imageOf(r.reasonPod)}</div>
              )}
              {r.reason === "image" && rebuildFor(r.reasonPod) && (
                <div className="rn-detail row-inline">
                  {rebuildFor(r.reasonPod)!.phase === "running" ||
                  rebuildFor(r.reasonPod)!.phase === "restarting" ? (
                    <span className="chip-spin" />
                  ) : null}
                  {rebuildLabel(rebuildFor(r.reasonPod)!)}
                  {rebuildFor(r.reasonPod)!.error ? ` — ${rebuildFor(r.reasonPod)!.error}` : ""}
                </div>
              )}
            </div>
            <div className="rn-actions">
              {/* The fix, where the banner already says what is wrong. Someone
                  reading this should not have to find the row and press
                  something there instead. */}
              {canRebuild && r.reason === "image" && r.reasonPod && !rebuildFor(r.reasonPod) && (
                <button className="btn sm" disabled={busy} onClick={() => rebuildImage(r.reasonPod)}>
                  Rebuild image
                </button>
              )}
              {r.reasonPod && (
                <Link className="btn ghost sm" to={`/ns/${namespace}/pods/${r.reasonPod}`}>
                  Pod logs
                </Link>
              )}
              <button
                className="btn ghost sm"
                onClick={() => clearRestartWatch(restartKey(r.ns, r.kind, r.name))}
              >
                {r.phase === "stuck" ? "Stop watching" : "Dismiss"}
              </button>
            </div>
          </div>
        ))}

      {selectMode && (
        <div className="bulk-bar">
          <span>Selected: {selected.size}</span>
          <div className="row">
            <button className="btn ghost sm" onClick={() => setSelected(new Set())}>
              Clear selection
            </button>
            <button
              className="btn sm"
              disabled={selected.size === 0}
              onClick={() => nav(`/ns/${namespace}/logs?pods=${[...selected].join(",")}`)}
            >
              Logs of selected
            </button>
            <button className="btn danger sm" disabled={busy || selected.size === 0} onClick={restartSelected}>
              Restart selected
            </button>
          </div>
        </div>
      )}

      <div className="card no-pad">
        <table className="table">
          <thead>
            <tr>
              {selectMode && (
                <th className="check-col">
                  <input type="checkbox" checked={allSelected} onChange={toggleAll} />
                </th>
              )}
              <th
                className="sortable"
                onClick={() => setSortDir((d) => (d === "asc" ? "desc" : "asc"))}
                title="Sort by name"
              >
                Pod {sortDir === "asc" ? "▲" : "▼"}
              </th>
              <th>Status</th>
              <th>Ready</th>
              <th>Restarts</th>
              <th>Node</th>
              <th>Age</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((p) => {
              const rollout = rolloutFor(p.name);
              const rolling = rollout?.phase === "progressing" || rollout?.phase === "stuck";
              return (
                <tr key={p.name} className={selectMode && selected.has(p.name) ? "row-selected" : ""}>
                  {selectMode && (
                    <td className="check-col">
                      <input
                        type="checkbox"
                        checked={selected.has(p.name)}
                        onChange={() => toggle(p.name)}
                      />
                    </td>
                  )}
                  <td>
                    <Link className="link" to={`/ns/${namespace}/pods/${p.name}`}>
                      {p.name}
                    </Link>
                  </td>
                  <td>
                    <span className="status-cell">
                      <span className={statusClass(p.status)}>{p.status}</span>
                      {rollout?.phase === "done" ? (
                        <span className="chip-restart done">restarted</span>
                      ) : rollout?.phase === "stuck" ? (
                        <span className="chip-restart stuck">
                          <span className="chip-spin" />
                          stuck
                        </span>
                      ) : rollout?.phase === "progressing" ? (
                        <span className="chip-restart" title={`${rollout.ready}/${rollout.desired} ready`}>
                          <span className="chip-spin" />
                          restarting
                        </span>
                      ) : null}
                      {/* The warning icon is hidden only while a rollout is
                          progressing normally — transient ContainerCreating
                          states are expected there. Real problems still show. */}
                      {/* A rebuild takes minutes and the natural thing to do
                          meanwhile is look at the list. Without this the only
                          sign of it is inside a dialog nobody keeps open. */}
                      {rebuildFor(p.name) && (
                        <span
                          className={
                            "chip-restart" +
                            (rebuildFor(p.name)!.phase === "failed" ? " stuck" : "") +
                            (rebuildFor(p.name)!.phase === "done" ? " done" : "")
                          }
                          title={rebuildFor(p.name)!.error || rebuildFor(p.name)!.status}
                        >
                          {rebuildFor(p.name)!.phase === "running" ||
                          rebuildFor(p.name)!.phase === "restarting" ? (
                            <span className="chip-spin" />
                          ) : null}
                          {rebuildLabel(rebuildFor(p.name)!)}
                        </span>
                      )}
                      {unhealthy(p) && rollout?.phase !== "progressing" && rollout?.phase !== "done" && (
                        <PodEvents namespace={namespace} pod={p.name} />
                      )}
                    </span>
                  </td>
                  <td>{p.ready}</td>
                  <td className={p.restarts > 0 ? "warn-text" : ""}>{p.restarts}</td>
                  <td className="muted">{p.node}</td>
                  <td className="muted">{age(p.age)}</td>
                  <td className="right">
                    <span className="action-cell">
                      {/* Offered here as well as inside the events dialog: this
                          is the fix for the state the row is already showing,
                          and having to open a dialog to reach it is a step
                          that carries no information. */}
                      {canRebuild && p.imageUnavailable && !rebuildFor(p.name) && (
                        <button
                          className="btn sm"
                          onClick={() => rebuildImage(p.name)}
                          disabled={busy}
                          title="Run the pipeline job that built this image again"
                        >
                          Rebuild image
                        </button>
                      )}
                      <button
                        className="btn danger sm"
                        onClick={() => restart(p.name)}
                        disabled={rolling}
                        title={rolling ? "A rollout is already in progress" : undefined}
                      >
                        Restart
                      </button>
                      <InfoHint text="Performs a rolling restart of the pod's workload (like kubectl rollout restart): the current pod keeps serving traffic until the replacement passes its readiness checks, so there is no downtime. If the new pod fails to start, the old one stays." />
                    </span>
                  </td>
                </tr>
              );
            })}
            {!loading && filtered.length === 0 && (
              <tr>
                <td colSpan={selectMode ? 8 : 7} className="muted center">
                  {pods.length > 0 ? "No pods match the filter" : "No pods in this namespace"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {loading && <div className="muted center pad">Loading…</div>}
      </div>
    </div>
  );
}
