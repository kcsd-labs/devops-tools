import { useParams, useSearchParams, Link } from "react-router-dom";
import { LogViewer } from "../components/LogViewer";

/** Merged logs of several pods, selected from the pod list. */
export function MultiLogs() {
  const { namespace = "" } = useParams();
  const [params] = useSearchParams();
  const pods = (params.get("pods") ?? "")
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean);

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <Link to={`/ns/${namespace}/pods`} className="crumb">
            ← {namespace}
          </Link>
          <h1 className="page-title">
            Logs · {pods.length} {pods.length === 1 ? "pod" : "pods"}
          </h1>
          <div className="muted mono">{pods.join(", ")}</div>
        </div>
      </div>

      {pods.length === 0 ? (
        <div className="alert">No pods selected.</div>
      ) : (
        <LogViewer namespace={namespace} pods={pods} />
      )}
    </div>
  );
}
