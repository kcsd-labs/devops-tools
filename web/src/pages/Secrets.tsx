import { useCallback, useEffect, useRef, useState } from "react";
import { useApi } from "../useApi";
import { useScrollLock } from "../useScrollLock";
import type { SecretSummary, SecretData } from "../api";
import { AutoTextarea } from "../components/AutoTextarea";
import { useDialogs } from "../components/Dialogs";

// Sentinel for the picker entry that switches to free text. Not a namespace
// name Kubernetes could ever produce, so it cannot collide with a real one.
const OTHER = "__other__";

type Row = { key: string; value: string };

export function Secrets() {
  const api = useApi();

  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [wildcard, setWildcard] = useState(false);
  // Switches the picker to free text so a namespace outside the list can be
  // opened. Only reachable by a wildcard role, which is the only one that has
  // access to anything beyond the list in the first place.
  const [typing, setTyping] = useState(false);
  const [ns, setNs] = useState("");

  const [secrets, setSecrets] = useState<SecretSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [hideSystem, setHideSystem] = useState(true);
  const [search, setSearch] = useState("");

  // Create / edit dialog
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<string | null>(null); // null means "create"
  const [name, setName] = useState("");
  const [rows, setRows] = useState<Row[]>([{ key: "", value: "" }]);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");
  const [dirty, setDirty] = useState(false); // unsaved edits, guards against losing them
  const [editMode, setEditMode] = useState<"kv" | "json">("kv");
  const [jsonText, setJsonText] = useState("");

  // Read-only view
  const [viewing, setViewing] = useState<SecretData | null>(null);
  const [viewError, setViewError] = useState("");
  const [viewMode, setViewMode] = useState<"kv" | "json">("kv");

  // Export / import between environments
  const [exp, setExp] = useState<SecretData | null>(null);
  const [expSelected, setExpSelected] = useState<Record<string, boolean>>({});
  const [expWithValues, setExpWithValues] = useState(true);
  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState("");
  const [importError, setImportError] = useState("");

  const { dialogs, confirm, alertDlg, toast } = useDialogs();

  useEffect(() => {
    api
      .namespaces()
      .then((r) => {
        setWildcard(r.wildcard);
        setNamespaces(r.namespaces);
        // Keep the current selection: the client is recreated when the token
        // is renewed, and resetting here would throw the user back to the
        // first namespace mid-task. A wildcard role may also be on a namespace
        // that is not in the list at all, which is equally worth keeping.
        setNs((cur) => {
          if (cur && (r.wildcard || r.namespaces.includes(cur))) return cur;
          return r.namespaces[0] || "";
        });
      })
      .catch((e) => setError(e.message));
  }, [api]);

  // Requests always use the current token, but a renewal must not rebuild
  // loadSecrets and reload the list — that showed up as flickering.
  const apiRef = useRef(api);
  apiRef.current = api;

  const loadSecrets = useCallback(() => {
    if (!ns) return;
    setLoading(true);
    setError("");
    apiRef.current
      .secrets(ns, !hideSystem) // system secrets are filtered server-side
      .then(setSecrets)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, [ns, hideSystem]);

  useEffect(loadSecrets, [loadSecrets]);

  useScrollLock(open || !!viewing || !!exp || importOpen);

  const visible = search
    ? secrets.filter(
        (s) =>
          s.name.toLowerCase().includes(search.toLowerCase()) ||
          s.keys.some((k) => k.toLowerCase().includes(search.toLowerCase()))
      )
    : secrets;

  const setRow = (i: number, patch: Partial<Row>) => {
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
    setDirty(true);
  };
  const addRow = () => {
    setRows((rs) => [...rs, { key: "", value: "" }]);
    setDirty(true);
  };
  const removeRow = (i: number) => {
    setRows((rs) => rs.filter((_, idx) => idx !== i));
    setDirty(true);
  };

  const rowsToData = (rs: Row[]): Record<string, string> => {
    const data: Record<string, string> = {};
    for (const r of rs) if (r.key.trim()) data[r.key.trim()] = r.value;
    return data;
  };

  /** Switches the editor between the key/value form and raw JSON. */
  const toggleMode = () => {
    if (editMode === "kv") {
      setJsonText(JSON.stringify(rowsToData(rows), null, 2));
      setEditMode("json");
      setFormError("");
      return;
    }
    try {
      const obj = JSON.parse(jsonText || "{}");
      if (typeof obj !== "object" || Array.isArray(obj)) throw new Error("expected an object");
      const parsed = Object.entries(obj).map(([key, value]) => ({
        key,
        value: value == null ? "" : String(value),
      }));
      setRows(parsed.length ? parsed : [{ key: "", value: "" }]);
      setEditMode("kv");
      setFormError("");
    } catch (e: any) {
      setFormError("Invalid JSON: " + e.message);
    }
  };

  const closeEditor = async () => {
    if (dirty) {
      const ok = await confirm({
        title: "Unsaved changes",
        message: "This secret has unsaved changes. Close without saving?",
        okText: "Discard",
        danger: true,
      });
      if (!ok) return;
    }
    setOpen(false);
  };

  const openCreate = () => {
    setEditing(null);
    setName("");
    setRows([{ key: "", value: "" }]);
    setFormError("");
    setDirty(false);
    setEditMode("kv");
    setOpen(true);
  };

  const openEdit = async (secretName: string) => {
    setEditing(secretName);
    setName(secretName);
    setFormError("");
    setDirty(false);
    setEditMode("kv");
    setOpen(true);
    try {
      const sec = await api.secret(ns, secretName);
      const loaded = Object.entries(sec.data).map(([key, value]) => ({ key, value }));
      setRows(loaded.length ? loaded : [{ key: "", value: "" }]); // loading is not an edit
    } catch (e: any) {
      setFormError(e.message);
    }
  };

  const openView = async (secretName: string) => {
    setViewError("");
    setViewMode("kv");
    setViewing({ name: secretName, type: "", data: {} }); // show the dialog, then fill it
    try {
      setViewing(await api.secret(ns, secretName));
    } catch (e: any) {
      setViewError(e.message);
    }
  };

  const save = async () => {
    let data: Record<string, string>;
    if (editMode === "json") {
      try {
        const obj = JSON.parse(jsonText || "{}");
        if (typeof obj !== "object" || Array.isArray(obj)) throw new Error("expected an object");
        data = Object.fromEntries(
          Object.entries(obj).map(([k, v]) => [k, v == null ? "" : String(v)])
        );
      } catch (e: any) {
        return setFormError("Invalid JSON: " + e.message);
      }
    } else {
      data = rowsToData(rows);
    }
    if (!editing && !name.trim()) return setFormError("Give the secret a name");
    if (Object.keys(data).length === 0) return setFormError("Add at least one key");

    setBusy(true);
    try {
      if (editing) await api.updateSecret(ns, editing, data);
      else await api.createSecret(ns, { name: name.trim(), data });
      setOpen(false);
      loadSecrets();
    } catch (e: any) {
      setFormError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (secretName: string) => {
    const ok = await confirm({
      title: "Delete secret",
      message: `Delete the secret ${secretName}? This cannot be undone.`,
      okText: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteSecret(ns, secretName);
      loadSecrets();
    } catch (e: any) {
      void alertDlg(e.message, "Error");
    }
  };

  // --- transfer between environments -----------------------------------------
  const openExport = async (secretName: string) => {
    try {
      const sec = await api.secret(ns, secretName);
      setExp(sec);
      setExpSelected(Object.fromEntries(Object.keys(sec.data).map((k) => [k, true])));
      setExpWithValues(true);
    } catch (e: any) {
      void alertDlg(e.message, "Error");
    }
  };

  const exportPayload = (): string => {
    if (!exp) return "";
    const data: Record<string, string> = {};
    for (const k of Object.keys(exp.data)) {
      if (expSelected[k]) data[k] = expWithValues ? exp.data[k] : "";
    }
    return JSON.stringify({ name: exp.name, type: exp.type, data }, null, 2);
  };

  const copyExport = async () => {
    await navigator.clipboard.writeText(exportPayload());
    toast("Copied to clipboard");
  };

  const downloadExport = () => {
    const blob = new Blob([exportPayload()], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${exp!.name}.json`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const onImportFile = (file?: File) => {
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => setImportText(String(reader.result));
    reader.readAsText(file);
  };

  const applyImport = () => {
    try {
      const payload = JSON.parse(importText);
      if (!payload || typeof payload.data !== "object") throw new Error("no data field");
      setEditing(null);
      setName(typeof payload.name === "string" ? payload.name : "");
      const parsed = Object.entries(payload.data as Record<string, unknown>).map(([key, value]) => ({
        key,
        value: value == null ? "" : String(value),
      }));
      setRows(parsed.length ? parsed : [{ key: "", value: "" }]);
      setFormError("");
      setImportOpen(false);
      setImportText("");
      setImportError("");
      setDirty(true); // imported but not saved yet — guard against closing by accident
      setEditMode("kv");
      setOpen(true); // hand over to the create dialog, pre-filled
    } catch (e: any) {
      setImportError("Could not parse the JSON: " + e.message);
    }
  };

  return (
    <div className="page">
      {dialogs}
      <div className="page-head">
        <div>
          <div className="crumb">Secrets</div>
          <h1 className="page-title">Secrets</h1>
        </div>
        <div className="row">
          {typing || (wildcard && namespaces.length === 0) ? (
            // A wildcard role also reaches namespaces the list leaves out — ones
            // hidden by configuration, or created since the page loaded — so
            // there has to be a way to name one that is not on it.
            <input
              className="input sm"
              placeholder="namespace…"
              value={ns}
              autoFocus={typing}
              onChange={(e) => setNs(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && loadSecrets()}
            />
          ) : (
            <select
              className="input sm select"
              value={namespaces.includes(ns) ? ns : ""}
              onChange={(e) => {
                if (e.target.value === OTHER) {
                  setTyping(true);
                  setNs("");
                  return;
                }
                setNs(e.target.value);
              }}
            >
              {/* The current namespace is not always on the list: a wildcard
                  role may have arrived here from a link to a hidden one. */}
              {!namespaces.includes(ns) && <option value="">{ns || "select a namespace…"}</option>}
              {namespaces.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
              {wildcard && <option value={OTHER}>Other namespace…</option>}
            </select>
          )}
          <input
            className="input sm"
            placeholder="Filter secrets…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <label className="check" title="Hide Helm release data and service account tokens">
            <input
              type="checkbox"
              checked={hideSystem}
              onChange={(e) => setHideSystem(e.target.checked)}
            />
            Hide system secrets
          </label>
          <button className="btn ghost" disabled={!ns} onClick={() => setImportOpen(true)}>
            ⭱ Import
          </button>
          <button className="btn" disabled={!ns} onClick={openCreate}>
            + New secret
          </button>
        </div>
      </div>

      {error && <div className="alert">{error}</div>}

      <div className="card no-pad">
        <table className="table">
          <thead>
            <tr>
              <th>Secret</th>
              <th>Type</th>
              <th>Keys</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {visible.map((sec) => (
              <tr key={sec.name}>
                <td>
                  <button
                    className="secret-name mono"
                    onClick={() => openView(sec.name)}
                    title="View this secret"
                  >
                    {sec.name}
                  </button>
                </td>
                <td className="muted">{sec.type}</td>
                <td className="muted">{sec.keys.join(", ")}</td>
                <td className="right">
                  <span className="action-cell">
                    <button className="btn ghost sm" onClick={() => openExport(sec.name)}>
                      Transfer
                    </button>
                    <button className="btn ghost sm" onClick={() => openEdit(sec.name)}>
                      Edit
                    </button>
                    <button className="btn danger sm" onClick={() => remove(sec.name)}>
                      Delete
                    </button>
                  </span>
                </td>
              </tr>
            ))}
            {!loading && ns && visible.length === 0 && (
              <tr>
                <td colSpan={4} className="muted center">
                  {secrets.length > 0
                    ? "Only system secrets here — untick the filter to see them"
                    : "No secrets in this namespace"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {loading && <div className="muted center pad">Loading…</div>}
      </div>

      {/* Create / edit. Clicking outside does not close it, so edits are not
          lost by a stray click. */}
      {open && (
        <div className="modal-overlay">
          <div className="modal secret-modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div>
                <div className="muted">{ns}</div>
                <div className="modal-title">{editing ? `Edit ${editing}` : "New secret"}</div>
              </div>
              <button className="icon-btn" onClick={closeEditor} disabled={busy}>
                ✕
              </button>
            </div>
            <div className="modal-body pad">
              {formError && <div className="alert">{formError}</div>}
              {!editing && (
                <div className="field">
                  <label className="muted">Name</label>
                  <input
                    className="input"
                    placeholder="my-secret"
                    value={name}
                    onChange={(e) => {
                      setName(e.target.value);
                      setDirty(true);
                    }}
                  />
                </div>
              )}
              <div className="row kv-head">
                <label className="muted">Data</label>
                <div className="seg" title="Editing mode">
                  <button
                    className={"seg-btn " + (editMode === "kv" ? "on" : "")}
                    onClick={() => editMode !== "kv" && toggleMode()}
                  >
                    Key / value
                  </button>
                  <button
                    className={"seg-btn " + (editMode === "json" ? "on" : "")}
                    onClick={() => editMode !== "json" && toggleMode()}
                  >
                    JSON
                  </button>
                </div>
              </div>

              {editMode === "json" ? (
                <textarea
                  className="input json-editor"
                  spellCheck={false}
                  value={jsonText}
                  onChange={(e) => {
                    setJsonText(e.target.value);
                    setDirty(true);
                  }}
                  placeholder={'{\n  "KEY": "value"\n}'}
                />
              ) : (
                <>
                  {rows.map((r, i) => (
                    <div className="row kv-row" key={i}>
                      <input
                        className="input"
                        placeholder="key"
                        value={r.key}
                        onChange={(e) => setRow(i, { key: e.target.value })}
                      />
                      <AutoTextarea
                        className="input mono kv-value"
                        placeholder="value"
                        value={r.value}
                        onChange={(v) => setRow(i, { value: v })}
                      />
                      <button className="btn ghost sm" title="Remove this key" onClick={() => removeRow(i)}>
                        ✕
                      </button>
                    </div>
                  ))}
                  <button className="btn ghost sm" onClick={addRow}>
                    + Add key
                  </button>
                </>
              )}
              <div className="modal-actions">
                <button className="btn ghost" onClick={closeEditor} disabled={busy}>
                  Cancel
                </button>
                <button className="btn" onClick={save} disabled={busy}>
                  {editing ? "Save" : "Create"}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Read-only view — safe to dismiss by clicking outside. */}
      {viewing && (
        <div className="modal-overlay" onClick={() => setViewing(null)}>
          <div className="modal secret-modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div>
                <div className="muted">{ns}</div>
                <div className="modal-title">{viewing.name}</div>
              </div>
              <button className="icon-btn" onClick={() => setViewing(null)}>
                ✕
              </button>
            </div>
            <div className="modal-body pad">
              {viewError && <div className="alert">{viewError}</div>}
              <div className="row kv-head">
                <label className="muted">Data</label>
                <div className="seg" title="View mode">
                  <button
                    className={"seg-btn " + (viewMode === "kv" ? "on" : "")}
                    onClick={() => setViewMode("kv")}
                  >
                    Key / value
                  </button>
                  <button
                    className={"seg-btn " + (viewMode === "json" ? "on" : "")}
                    onClick={() => setViewMode("json")}
                  >
                    JSON
                  </button>
                </div>
              </div>
              {Object.keys(viewing.data).length === 0 && !viewError ? (
                <div className="muted">Loading…</div>
              ) : viewMode === "json" ? (
                <textarea
                  className="input json-editor"
                  spellCheck={false}
                  readOnly
                  value={JSON.stringify(viewing.data, null, 2)}
                />
              ) : (
                Object.entries(viewing.data).map(([k, v]) => (
                  <div className="row kv-row" key={k}>
                    <input className="input mono" value={k} readOnly />
                    <AutoTextarea className="input mono kv-value" value={v} readOnly />
                  </div>
                ))
              )}
              <div className="modal-actions">
                <button className="btn ghost" onClick={() => setViewing(null)}>
                  Close
                </button>
                <button
                  className="btn"
                  onClick={() => {
                    const n = viewing.name;
                    setViewing(null);
                    openEdit(n);
                  }}
                >
                  Edit
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Transfer: export a secret so it can be recreated in another environment. */}
      {exp && (
        <div className="modal-overlay" onClick={() => setExp(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div>
                <div className="muted">Transfer · export from {ns}</div>
                <div className="modal-title mono">{exp.name}</div>
              </div>
              <button className="icon-btn" onClick={() => setExp(null)}>
                ✕
              </button>
            </div>
            <div className="modal-body pad">
              <p className="muted">
                Pick the keys to carry over, choose whether to include the values, then copy or
                download. In the target environment open Secrets → <b>Import</b> and paste it.
              </p>

              <label className="check">
                <input
                  type="checkbox"
                  checked={expWithValues}
                  onChange={(e) => setExpWithValues(e.target.checked)}
                />
                Include values (otherwise only the keys are carried over, with empty values)
              </label>

              <div className="key-list">
                {Object.keys(exp.data).map((k) => (
                  <label className="check" key={k}>
                    <input
                      type="checkbox"
                      checked={!!expSelected[k]}
                      onChange={(e) => setExpSelected((s) => ({ ...s, [k]: e.target.checked }))}
                    />
                    <span className="mono">{k}</span>
                  </label>
                ))}
              </div>

              <div className="modal-actions">
                <button className="btn ghost" onClick={downloadExport}>
                  ⭳ Download .json
                </button>
                <button className="btn" onClick={copyExport}>
                  Copy
                </button>
              </div>
            </div>
          </div>
        </div>
      )}

      {importOpen && (
        <div className="modal-overlay" onClick={() => setImportOpen(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div>
                <div className="muted">Import into {ns}</div>
                <div className="modal-title">Import a secret</div>
              </div>
              <button className="icon-btn" onClick={() => setImportOpen(false)}>
                ✕
              </button>
            </div>
            <div className="modal-body pad">
              {importError && <div className="alert">{importError}</div>}
              <p className="muted">
                Paste the JSON produced by Transfer in another environment, or load it from a file.
              </p>
              <input
                type="file"
                accept=".json,application/json"
                onChange={(e) => onImportFile(e.target.files?.[0])}
              />
              <textarea
                className="input import-text"
                placeholder='{ "name": "...", "data": { "KEY": "value" } }'
                value={importText}
                onChange={(e) => setImportText(e.target.value)}
              />
              <div className="modal-actions">
                <button className="btn ghost" onClick={() => setImportOpen(false)}>
                  Cancel
                </button>
                <button className="btn" disabled={!importText.trim()} onClick={applyImport}>
                  Continue →
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
