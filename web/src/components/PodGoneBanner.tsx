import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useApi } from "../useApi";

/**
 * Shown when the pod being viewed no longer exists — usually because a
 * deployment replaced it. Better than an unexplained error, and it offers the
 * obvious next step: jump to the pod that took its place.
 *
 * Finding the replacement needs the service label, which the caller captured
 * while the pod was still alive.
 */
export function PodGoneBanner({
  namespace,
  appLabel,
  secondaryLabel,
  onSecondary,
}: {
  namespace: string;
  appLabel: string;
  secondaryLabel?: string;
  onSecondary?: () => void;
}) {
  const api = useApi();
  const nav = useNavigate();
  const [msg, setMsg] = useState("");

  const openNewPod = async () => {
    if (!appLabel) return;
    try {
      const list = await api.workloadPods(namespace, appLabel);
      if (list.length) nav(`/ns/${namespace}/pods/${list[0]}`);
      else setMsg("No running pod found for this service.");
    } catch (e: any) {
      setMsg(e.message);
    }
  };

  return (
    <div className="alert alert-action">
      <b>This pod is gone.</b> It no longer exists in the cluster — most likely it was replaced by a
      new deployment.
      <div className="row gone-actions">
        {appLabel && (
          <button className="btn sm" onClick={openNewPod}>
            Open the current pod
          </button>
        )}
        {secondaryLabel && onSecondary && (
          <button className="btn ghost sm" onClick={onSecondary}>
            {secondaryLabel}
          </button>
        )}
      </div>
      {msg && <div className="muted gone-msg">{msg}</div>}
    </div>
  );
}
