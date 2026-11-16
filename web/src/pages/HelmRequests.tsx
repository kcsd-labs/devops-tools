import { useCallback, useEffect, useRef, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { useApi } from "../useApi";
import { useEditorTheme } from "../editorTheme";
import { CatalogError, type CatalogCheck, type HelmRequest, type RoleGrant } from "../api";
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
  // A new namespace: which of the requester's roles get it.
  const [grants, setGrants] = useState<RoleGrant[] | null>(null);
  const [granted, setGranted] = useState<Set<string>>(new Set());
  const [showNotes, setShowNotes] = useState(false);

  useEffect(() => {
    if (!q.newNamespace) return;
    api
      .requestGrants(q.id)
      .then((g) => {
        setGrants(g);
        setGranted(new Set(g.filter((x) => !x.covered && x.operations.length > 0).map((x) => x.role)));
      })
      .catch((e) => setError(e.message));
  }, [api, q.id, q.newNamespace]);

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
      onAnswered(await api.approveRequest(q.id, values, Array.from(granted)));
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
      <div className="modal cat-review" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <div className="muted">
              requested by {q.requester} · {ago(q.created)} · chart {q.digest.slice(0, 19)}…
            </div>
            <div className="modal-title">
              <span className="mono">{q.release}</span> → <span className="mono">{q.namespace}</span>{" "}
              <span className="cm-pill">{q.kind}</span>
              {q.newNamespace && <span className="tag">new namespace</span>}
            </div>
            <div className="muted">{requestLine(q)}</div>
          </div>
          <button className="icon-btn" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="modal-body pad">
          {q.newNamespace && (
            <div className="cat-grant">
              <div className="cat-q">
                {q.namespace} does not exist yet — it is created when you approve
              </div>
              {grants === null && <div className="muted">Reading {q.requester}'s roles…</div>}
              {grants !== null && grants.length === 0 && (
                <div className="cat-why">
                  {q.requester} holds no role to give it to. Give access later, under Access management — until then they
                  will not see the release.
                </div>
              )}
              {grants !== null && grants.length > 0 && (
                <>
                  <div className="cat-why">
                    Give {q.requester}'s roles the new namespace, so they can follow and manage the release. Each gets the
                    operations it already holds in its other namespaces.
                  </div>
                  {grants.map((g) => (
                    <label className="checkbox cat-grant-row" key={g.role}>
                      <input
                        type="checkbox"
                        disabled={g.covered || g.operations.length === 0}
                        checked={granted.has(g.role)}
                        onChange={(e) =>
                          setGranted((cur) => {
                            const next = new Set(cur);
                            if (e.target.checked) next.add(g.role);
                            else next.delete(g.role);
                            return next;
                          })
                        }
                      />
                      <span>
                        <b className="mono">{g.role}</b>{" "}
                        <span className="muted">
                          {g.covered
                            ? "— reaches every namespace already"
                            : g.operations.length === 0
                              ? "— names no namespace to copy from"
                              : `— ${g.operations.join(", ")}`}
                        </span>
                      </span>
                    </label>
                  ))}
                </>
              )}
            </div>
          )}
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
          {check?.ok && check.notes && (
            <div className="cat-review-notes">
              <button className="link-btn" onClick={() => setShowNotes((x) => !x)}>
                {showNotes ? "▾" : "▸"} What {q.requester} will get, as the chart puts it
              </button>
              {showNotes && <pre className="cat-notes-text cat-readme">{check.notes}</pre>}
            </div>
          )}

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
            {busy
              ? "Working…"
              : changed
                ? "Approve with my changes"
                : q.newNamespace
                  ? "Create the namespace and install"
                  : "Approve and install"}
          </button>
        </div>
      </div>
    </div>
  );
}
