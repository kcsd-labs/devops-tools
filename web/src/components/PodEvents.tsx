import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useApi } from "../useApi";
import { useScrollLock } from "../useScrollLock";
import type { PodEvent } from "../api";
import {
  getRebuild,
  subscribeRebuilds,
  watchBuild,
  watchPipeline,
  type RebuildState,
} from "../rebuildWatcher";

function age(iso: string) {
  const t = new Date(iso).getTime();
  if (!t) return "";
  const d = (Date.now() - t) / 1000;
  if (d < 60) return `${Math.floor(d)}s`;
  if (d < 3600) return `${Math.floor(d / 60)}m`;
  if (d < 86400) return `${Math.floor(d / 3600)}h`;
  return `${Math.floor(d / 86400)}d`;
}

/**
 * Warning icon for an unhealthy pod. Clicking it shows the pod's events and,
 * for the two failure modes that are easy to misread, an explanation of what
 * is actually wrong.
 */
export function PodEvents({ namespace, pod }: { namespace: string; pod: string }) {
  const api = useApi();
  const [open, setOpen] = useState(false);
  const [events, setEvents] = useState<PodEvent[]>([]);
  const [imageUnavailable, setImageUnavailable] = useState(false);
  const [crashLooping, setCrashLooping] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  // What this person may do about it, in this namespace. Both false where the
  // deployment does not manage builds at all.
  const [canRebuild, setCanRebuild] = useState(false);
  const [canDeploy, setCanDeploy] = useState(false);
  const [busy, setBusy] = useState(false);
  // Set when the pipeline that built this image is gone, so the only way back
  // is a fresh one — which also deploys.
  const [pipelineGone, setPipelineGone] = useState<{ branch: string; message: string } | null>(null);

  // The build runs outside React and outlives this dialog, so its progress is
  // read from the watcher. Subscribed rather than read on every render: the
  // watcher hands back a fresh object each time, which as a render-time read
  // would never compare equal and would never stop re-rendering.
  const [rebuild, setRebuild] = useState(() => getRebuild(namespace, pod));
  useEffect(() => {
    const sync = () => setRebuild(getRebuild(namespace, pod));
    sync();
    return subscribeRebuilds(sync);
  }, [namespace, pod]);

  useScrollLock(open);

  const show = () => {
    setOpen(true);
    setLoading(true);
    setError("");
    api
      .podEvents(namespace, pod)
      .then((d) => {
        setEvents(d.events);
        setImageUnavailable(d.imageUnavailable);
        setCrashLooping(d.crashLooping);
        setCanRebuild(d.canRebuildImage ?? false);
        setCanDeploy(d.canDeploy ?? false);
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  };

  const rebuildImage = async () => {
    setBusy(true);
    setError("");
    try {
      const d = await api.rebuildImage(namespace, pod);
      if (d.pipelineGone) {
        setPipelineGone({ branch: d.branch ?? "", message: d.message ?? "" });
        return;
      }
      watchBuild(namespace, pod, d.ticket!, d.jobStatus ?? "", d.jobUrl ?? "");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const startPipeline = async () => {
    setBusy(true);
    setError("");
    try {
      const d = await api.startRebuildPipeline(namespace, pod);
      setPipelineGone(null);
      watchPipeline(namespace, pod, d.ticket, d.status ?? "", d.pipelineUrl ?? "");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button
        className="icon-warn"
        title="This pod is unhealthy — show events"
        onClick={(e) => {
          e.stopPropagation();
          show();
        }}
      >
        ⚠
      </button>

      {open && (
        <div className="modal-overlay" onClick={() => setOpen(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div className="modal-title">Events — {pod}</div>
              <button className="btn ghost sm" onClick={() => setOpen(false)}>
                ✕
              </button>
            </div>
            <div className="modal-body pad">
              {/* The flags come from the pod's current container state, not from
                  the event history, so a banner never lingers after the problem
                  has actually been resolved. */}
              {imageUnavailable && (
                <div className="alert alert-action">
                  <b>The image cannot be pulled.</b> The tag is missing from the registry, or the
                  pull credentials are wrong. Restarting will not help until the image is available
                  again — check that the tag still exists and that the pull secret is valid.
                  {/* Where the tag names the pipeline that built it, the
                      missing tag can simply be built again. Only offered to
                      someone who may actually do it, in this namespace. */}
                  {canRebuild && !rebuild && !pipelineGone && (
                    <div className="row banner-actions">
                      <button className="btn sm" disabled={busy} onClick={rebuildImage}>
                        {busy ? "Starting…" : "Rebuild image"}
                      </button>
                    </div>
                  )}
                  {rebuild && <RebuildProgress state={rebuild} />}
                  {pipelineGone && (
                    <div className="banner-actions">
                      <p>{pipelineGone.message}</p>
                      {canDeploy ? (
                        <button className="btn sm" disabled={busy} onClick={startPipeline}>
                          {busy ? "Starting…" : `Run a new pipeline on ${pipelineGone.branch}`}
                        </button>
                      ) : (
                        // Said plainly rather than hidden: knowing what would
                        // fix this is worth having even when you cannot do it.
                        <p className="muted">
                          That is a deployment, and your roles do not cover it. Ask someone who can
                          deploy to {pipelineGone.branch}.
                        </p>
                      )}
                    </div>
                  )}
                </div>
              )}

              {!imageUnavailable && crashLooping && (
                <div className="alert alert-action">
                  <b>The container keeps crashing on startup</b> (CrashLoopBackOff). The image was
                  pulled, so the problem is in the application or its configuration. Check the{" "}
                  <Link to={`/ns/${namespace}/pods/${pod}`} onClick={() => setOpen(false)}>
                    pod logs
                  </Link>
                  .
                </div>
              )}

              {loading ? (
                <div className="muted">Loading…</div>
              ) : error ? (
                <div className="alert">{error}</div>
              ) : events.length === 0 ? (
                <div className="muted">No events recorded for this pod.</div>
              ) : (
                <table className="table events-table">
                  <thead>
                    <tr>
                      <th>Type</th>
                      <th>Reason</th>
                      <th>Message</th>
                      <th>×</th>
                      <th>When</th>
                    </tr>
                  </thead>
                  <tbody>
                    {events.map((e, i) => (
                      <tr key={i} className={e.type === "Warning" ? "ev-warn" : ""}>
                        <td>{e.type}</td>
                        <td>{e.reason}</td>
                        <td className="ev-msg">{e.message}</td>
                        <td className="muted">{e.count > 1 ? e.count : ""}</td>
                        <td className="muted">{age(e.last)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );
}

// How the build is getting on. Deliberately plain: the interesting detail lives
// in GitLab, and the link is always there rather than only on failure.
function RebuildProgress({ state }: { state: RebuildState }) {
  const link = state.url ? (
    <a href={state.url} target="_blank" rel="noreferrer">
      Open in GitLab
    </a>
  ) : null;

  if (state.phase === "running" || state.phase === "restarting") {
    return (
      <div className="banner-actions">
        <p className="row-inline">
          <span className="chip-spin" />
          {state.phase === "restarting"
            ? "Image rebuilt — restarting the workload…"
            : `${state.kind === "pipeline" ? "Pipeline" : "Build"} ${state.status || "starting"}…`}{" "}
          {link}
        </p>
        <p className="muted">
          You can leave this page — it keeps watching, and tells you when it finishes.
        </p>
      </div>
    );
  }
  if (state.phase === "done") {
    return (
      <div className="banner-actions">
        <p>Finished. {link}</p>
      </div>
    );
  }
  // Failed, or waiting for a person. Both leave the text to say which.
  return (
    <div className="banner-actions">
      <p>{state.error}</p>
      {link}
    </div>
  );
}
