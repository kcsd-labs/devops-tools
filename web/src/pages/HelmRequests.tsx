import { useCallback, useEffect, useRef, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { useApi } from "../useApi";
import { useEditorTheme } from "../editorTheme";
import { CatalogError, type CatalogCheck, type HelmRequest } from "../api";
import { CheckResult, Loading, RequestStatus, ago, requestLine, requestNote } from "../components/Catalog";
import { pushToast } from "../toasts";

// Requests to install or upgrade from the catalog, for the administrators who
// answer them. Every namespace's in one list: answering is a global grant.

export function HelmRequests() {
  const api = useApi();
  const [list, setList] = useState<HelmRequest[] | null>(null);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<"waiting" | "history">("waiting");
  const [open, setOpen] = useState<HelmRequest | null>(null);

  const load = useCallback(() => {
    api
      .allRequests()
      .then((l) => {
        setList(l);
        setError("");
      })
      .catch((e) => setError(e.message));
  }, [api]);

  useEffect(load, [load]);
  useEffect(() => {
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [load]);

  const waiting = (list ?? []).filter((q) => q.status === "waiting");
  const history = (list ?? []).filter((q) => q.status !== "waiting");
  const shown = tab === "waiting" ? waiting : history;

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">Install requests</h1>
          <div className="seg-switch" role="tablist">
            <button className={tab === "waiting" ? "on" : ""} onClick={() => setTab("waiting")}>
              Waiting{waiting.length ? ` (${waiting.length})` : ""}
            </button>
            <button className={tab === "history" ? "on" : ""} onClick={() => setTab("history")}>
              History
            </button>
          </div>
          <div className="muted svc-sub">Nothing from the catalog is installed until you approve it</div>
        </div>
        <button className="btn ghost" onClick={load}>
          Refresh
        </button>
      </div>

      {error && <div className="alert">{error}</div>}
      <div className="card no-pad">
        {list === null && !error && <Loading text="Loading the requests…" />}
        {list !== null && shown.length === 0 && (
          <div className="muted center pad">{tab === "waiting" ? "Nothing waits for an answer" : "No answered requests yet"}</div>
        )}
        <div className="cat-list">
          {shown.map((q) => (
            <div className="cat-row" key={q.id}>
              <div className="cat-main">
                <div>
                  <b className="mono">{q.release}</b> → <span className="mono">{q.namespace}</span>{" "}
                  <span className="muted">{requestLine(q)}</span>
                </div>
                <div className={"cat-desc-line " + (q.status === "failed" ? "cat-failed" : "muted")}>
                  {q.status === "waiting" ? requestNote(q) : `${q.requester} · ${ago(q.created)} · ${requestNote(q)}`}
                </div>
              </div>
              <RequestStatus r={q} />
              {q.status === "waiting" && (
                <button className="btn sm" onClick={() => setOpen(q)}>
                  Open
                </button>
              )}
            </div>
          ))}
        </div>
      </div>

      {open && (
        <Review
          request={open}
          onClose={() => setOpen(null)}
          onAnswered={(q) => {
            setOpen(null);
            load();
            window.dispatchEvent(new Event("devops-tools:requests-changed"));
            pushToast(
              q.status === "rejected" ? `Rejected ${q.release}` : `Approved — ${q.release} is being installed`,
              "ok"
            );
          }}
        />
      )}
    </div>
  );
}

function Review({
  request: q,
  onClose,
  onAnswered,
}: {
  request: HelmRequest;
  onClose: () => void;
  onAnswered: (q: HelmRequest) => void;
}) {
  const api = useApi();
  const theme = useEditorTheme();
  const [values, setValues] = useState(q.values);
  const [check, setCheck] = useState<CatalogCheck>();
  const [checkedFor, setCheckedFor] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [comment, setComment] = useState("");
  const [commentError, setCommentError] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const seq = useRef(0);
  useEffect(() => {
    const n = ++seq.current;
    setChecking(true);
    const t = setTimeout(() => {
      api
        .checkRequest(q.id, values)
        .then((c) => {
          if (n !== seq.current) return;
          setCheck(c);
          setCheckedFor(values);
          setCheckError("");
        })
        .catch((e) => n === seq.current && (setCheckError(e.message), setCheck(undefined)))
        .finally(() => n === seq.current && setChecking(false));
    }, 700);
    return () => clearTimeout(t);
  }, [api, q.id, values]);

  const approve = async () => {
    setBusy(true);
    setError("");
    try {
      onAnswered(await api.approveRequest(q.id, values));
    } catch (e: any) {
      if (e instanceof CatalogError && e.check) setCheck(e.check);
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const reject = async () => {
    if (!comment.trim()) {
      setCommentError("Say why, so the developer knows what to change");
      return;
    }
    setBusy(true);
    setError("");
    try {
      onAnswered(await api.rejectRequest(q.id, comment));
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const changed = values.trim() !== q.values.trim();
  const ready = !!check?.ok && checkedFor === values && !checking && !busy;

  return (
    <div className="modal-overlay">
      <div className="modal cat-install" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <div className="muted">
              requested by {q.requester} · {ago(q.created)} · chart {q.digest.slice(0, 19)}…
            </div>
            <div className="modal-title">
              <span className="mono">{q.release}</span> → <span className="mono">{q.namespace}</span>{" "}
              <span className="cm-pill">{q.kind}</span>
            </div>
            <div className="muted">{requestLine(q)}</div>
          </div>
          <button className="icon-btn" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="modal-body pad">
          <div className="field-hint">
            Values on top of the chart's defaults. Change them if you need to — the developer sees what you changed.
            {changed && (
              <>
                {" "}
                <button className="link-btn" onClick={() => setValues(q.values)}>
                  Undo my changes
                </button>
              </>
            )}
          </div>
          <CodeMirror value={values} theme={theme} height="240px" extensions={[yaml()]} onChange={setValues} />
          <CheckResult check={check} checking={checking} error={checkError} upgrade={q.kind === "upgrade"} />

          <div className="field" style={{ marginTop: 14 }}>
            <label className="muted">Comment for {q.requester} (needed to reject)</label>
            <input
              className="input"
              value={comment}
              placeholder="Use the shared Kafka in platform-dev"
              onChange={(e) => {
                setComment(e.target.value);
                setCommentError("");
              }}
            />
            {commentError && <div className="cat-failed">{commentError}</div>}
          </div>
          {error && <div className="alert">{error}</div>}
        </div>
        <div className="modal-actions pad">
          <button className="btn danger" disabled={busy} onClick={reject} style={{ marginRight: "auto" }}>
            Reject
          </button>
          <button className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn" disabled={!ready} onClick={approve}>
            {busy ? "Working…" : changed ? "Approve with my changes" : "Approve and install"}
          </button>
        </div>
      </div>
    </div>
  );
}
