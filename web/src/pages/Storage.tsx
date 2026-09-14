import { useEffect, useRef, useState } from "react";
import { useApi } from "../useApi";
import { AccessTabs } from "../components/AccessTabs";
import { useDialogs } from "../components/Dialogs";
import type { StorageStat } from "../api";

function kb(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KB`;
}

/** Where the model is kept, said the way somebody would say it aloud. */
function backendText(backend: string): { kind: string; where: string } {
  const [kind, ...rest] = backend.split(" ");
  return { kind: kind === "secret" ? "Kubernetes Secret" : "File", where: rest.join(" ") };
}

export function Storage() {
  const api = useApi();
  const apiRef = useRef(api);
  apiRef.current = api;

  const { dialogs, confirm, toast } = useDialogs();
  const [stat, setStat] = useState<StorageStat | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const load = () => {
    apiRef.current
      .storage()
      .then(setStat)
      .catch((e) => setError(e.message));
  };
  useEffect(load, []);

  const download = async () => {
    setBusy(true);
    try {
      const blob = await apiRef.current.snapshot();
      // The browser saves it; nothing is kept here.
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `devops-tools-access-${new Date().toISOString().slice(0, 10)}.json`;
      a.click();
      URL.revokeObjectURL(url);
      toast("Snapshot saved");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const restore = async (file: File) => {
    const text = await file.text();
    const ok = await confirm({
      title: "Replace the access model",
      message:
        `Everything in ${file.name} replaces what is stored now: every role, and everyone who ` +
        `holds one. The accounts in bootstrapAdmins are not part of this and keep working, ` +
        `which is the way back if this is the wrong file.`,
      okText: "Replace",
      danger: true,
    });
    if (!ok) return;

    setBusy(true);
    setError("");
    try {
      const res = await apiRef.current.restore(text);
      toast(`Restored ${res.users} users and ${res.roles} roles`);
      load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = ""; // so the same file can be picked again
    }
  };

  const where = stat ? backendText(stat.backend) : null;
  const share = stat && stat.limit > 0 ? Math.round((stat.bytes / stat.limit) * 100) : 0;

  return (
    <div className="page">
      <div className="page-head">
        <h1 className="page-title">Access management</h1>
      </div>
      <AccessTabs />

      {error && <div className="alert">{error}</div>}

      {stat && where && (
        <div className="grid" style={{ marginTop: 12 }}>
          <div className="card">
            <div className="card-title">Where it lives</div>
            <table className="table">
              <tbody>
                <tr>
                  <td className="muted">Kept in</td>
                  <td>{where.kind}</td>
                </tr>
                <tr>
                  <td className="muted">Object</td>
                  <td className="console">{where.where}</td>
                </tr>
                <tr>
                  <td className="muted">Size</td>
                  <td>
                    {/* What is stored, because that is what the ceiling is
                        about. The uncompressed figure is shown beside it only
                        when the two differ, so the usual case stays one number. */}
                    <span className={share > 70 ? "warn-text" : undefined}>{kb(stat.bytes)}</span>
                    {stat.limit > 0 && (
                      <span className="muted">
                        {" "}
                        of {kb(stat.limit)} · {share}%
                      </span>
                    )}
                    {stat.raw > stat.bytes && (
                      <div className="muted" style={{ fontSize: 12 }}>
                        {kb(stat.raw)} before compression
                      </div>
                    )}
                  </td>
                </tr>
                {stat.replicas !== undefined && (
                  <tr>
                    <td className="muted">Replicas</td>
                    <td>{stat.replicas}</td>
                  </tr>
                )}
                <tr>
                  <td className="muted">This replica</td>
                  <td>
                    {stat.current ? (
                      <span className="badge badge-ok">up to date</span>
                    ) : (
                      // Not an error: a change landed elsewhere a moment ago
                      // and is on its way here.
                      <span className="badge badge-warn">catching up</span>
                    )}
                  </td>
                </tr>
                <tr>
                  <td className="muted">Last written here</td>
                  <td>
                    {stat.lastWrite ? (
                      new Date(stat.lastWrite).toLocaleString()
                    ) : (
                      <span className="muted">not since this replica started</span>
                    )}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <div className="card">
            <div className="card-title">What is in it</div>
            <table className="table">
              <tbody>
                <tr>
                  <td className="muted">Users</td>
                  <td>{stat.users}</td>
                </tr>
                <tr>
                  <td className="muted">With a role</td>
                  <td>{stat.withRole}</td>
                </tr>
                <tr>
                  <td className="muted">Roles</td>
                  <td>{stat.roles}</td>
                </tr>
                <tr>
                  <td className="muted">Format</td>
                  <td className="console">version {stat.schema}</td>
                </tr>
              </tbody>
            </table>

            {stat.mayRestore && (
              <>
                <div className="row" style={{ gap: 8, marginTop: 12 }}>
                  <button className="btn" onClick={download} disabled={busy}>
                    Download snapshot
                  </button>
                  <button
                    className="btn ghost"
                    onClick={() => fileRef.current?.click()}
                    disabled={busy}
                  >
                    Restore…
                  </button>
                  <input
                    ref={fileRef}
                    type="file"
                    accept="application/json,.json"
                    style={{ display: "none" }}
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f) void restore(f);
                    }}
                  />
                </div>
                <p className="muted" style={{ marginTop: 10, marginBottom: 0 }}>
                  The snapshot is the same document that is stored. Keep it before a risky change
                  to permissions, or use it to carry an access model to another environment.
                </p>
              </>
            )}
          </div>
        </div>
      )}

      {dialogs}
    </div>
  );
}
