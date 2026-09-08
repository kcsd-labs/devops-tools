import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useApi } from "../useApi";
import { useScrollLock } from "../useScrollLock";
import { useDialogs } from "../components/Dialogs";
import { AccessTabs } from "../components/AccessTabs";
import type { ConfigGrant, Me, NamespaceGrant, Role } from "../api";

// What each role may do, and where.
//
// A role names namespaces and, within each, the operations it permits — which
// is how access to secret values is granted separately from access to logs in
// the same namespace. Who holds a role is decided under Users.
export function Roles() {
  const api = useApi();
  const [roles, setRoles] = useState<Role[]>([]);
  const [operations, setOperations] = useState<string[]>([]);
  const [namespaces, setNamespaces] = useState<string[]>([]);
  // How many people hold each role. Read-only here on purpose: this page is
  // about what a role permits, and who holds it is decided under Users.
  const [holders, setHolders] = useState<Record<string, number>>({});
  const [me, setMe] = useState<Me | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<Role | null>(null);
  const [creating, setCreating] = useState(false);

  const { dialogs, confirm, alertDlg, toast } = useDialogs();

  const apiRef = useRef(api);
  apiRef.current = api;

  const load = useCallback(() => {
    Promise.all([apiRef.current.roles(), apiRef.current.me(), apiRef.current.namespaces()])
      .then(([r, m, ns]) => {
        setRoles(r.roles);
        setOperations(r.operations);
        setHolders(r.holders ?? {});
        setMe(m);
        // Suggestions for the namespace field only. A role may name a namespace
        // this list leaves out, so the field stays free text.
        setNamespaces(ns.namespaces);
        setError("");
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoaded(true));
  }, []);

  useEffect(load, [load]);
  useScrollLock(creating || !!editing);

  const canManage = me?.capabilities?.manageUsers ?? false;

  const remove = async (role: Role) => {
    const ok = await confirm({
      title: `Delete the role ${role.name}?`,
      message: "Everyone holding it loses the access it granted, immediately.",
      okText: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      const res = await api.deleteRole(role.name);
      if (res.affectedUsers.length) {
        // Naming them beats leaving the loss of access to be discovered by
        // whoever lost it.
        await alertDlg(
          `These users lost the access it granted: ${res.affectedUsers.join(", ")}.`,
          "Role deleted"
        );
      } else {
        toast(`${role.name} deleted`);
      }
      load();
    } catch (e: any) {
      setError(e.message);
    }
  };

  return (
    <div className="page">
      {dialogs}

      <div className="page-head">
        <div className="row head-left">
          <h1 className="page-title">Access management</h1>
          <AccessTabs />
        </div>
        {canManage && (
          <button className="btn" onClick={() => setCreating(true)}>
            + New role
          </button>
        )}
      </div>

      {error && <div className="alert">{error}</div>}
      {!loaded && !error && <div className="muted">Loading…</div>}

      {loaded && !error && roles.length === 0 && (
        <div className="card">
          <div className="card-title">No roles yet</div>
          <p className="muted">
            A role grants operations in namespaces. Create one to hand out access.
          </p>
        </div>
      )}

      {roles.length > 0 && (
        <div className="card no-pad">
          <table className="table">
            <thead>
              <tr>
                <th>Role</th>
                <th>Held by</th>
                <th>Namespaces</th>
                <th>Portal administration</th>
                {canManage && <th className="right">Actions</th>}
              </tr>
            </thead>
            <tbody>
              {roles.map((r) => (
                <tr key={r.name}>
                  <td>
                    <div className="user-name">{r.name}</div>
                    {r.description && <div className="muted user-sub">{r.description}</div>}
                  </td>
                  <td>
                    {/* A count, and a way to see the names — which lives on the
                        users screen, where they can also be changed. */}
                    {holders[r.name] ? (
                      <Link className="link" to={`/access/users?role=${encodeURIComponent(r.name)}`}>
                        {holders[r.name]} {holders[r.name] === 1 ? "person" : "people"}
                      </Link>
                    ) : (
                      <span className="muted">nobody</span>
                    )}
                  </td>
                  <td>
                    {r.namespaces?.length ? (
                      <div className="grant-list">
                        {r.namespaces.map((g, i) => (
                          <div key={i} className="grant-row">
                            <span className="role-chip">{g.namespace}</span>
                            <span className="muted grant-ops">
                              {g.operations?.includes("*")
                                ? "everything"
                                : g.operations?.join(", ") || "nothing"}
                            </span>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <span className="muted">none</span>
                    )}
                  </td>
                  <td>
                    {r.global?.length ? (
                      <div className="role-chips inline">
                        {r.global.map((g) => (
                          <span key={g} className="role-chip">
                            {g}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </td>
                  {canManage && (
                    <td className="right nowrap">
                      <button className="btn ghost sm" onClick={() => setEditing(r)}>
                        Edit
                      </button>
                      <button className="btn danger sm" onClick={() => remove(r)}>
                        Delete
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(creating || editing) && (
        <RoleDialog
          role={editing}
          operations={operations}
          namespaceOptions={namespaces}
          existingNames={roles.map((r) => r.name)}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSaved={(name) => {
            setCreating(false);
            setEditing(null);
            toast(`${name} saved`);
            load();
          }}
        />
      )}
    </div>
  );
}

// GLOBAL_OPS govern the portal itself rather than a workload, so the editor
// lists them apart from the per-namespace ones.
const GLOBAL_OPS = ["user-list", "user-manage"];

// Configuration is granted by path rather than by namespace, so these are kept
// out of the per-namespace list too. The block appears only where the
// deployment declares them — an installation without the Configurations feature
// simply leaves them out of its operations, and never sees the section.
const CONFIG_OPS = ["config-read", "config-write"];

const CONFIG_OP_LABELS: Record<string, string> = {
  "config-read": "view",
  "config-write": "edit",
};

const GLOBAL_OP_LABELS: Record<string, string> = {
  "user-list": "see users and roles",
  "user-manage": "create users, set passwords, change roles",
};

function RoleDialog({
  role,
  operations,
  namespaceOptions,
  existingNames,
  onClose,
  onSaved,
}: {
  role: Role | null;
  operations: string[];
  namespaceOptions: string[];
  existingNames: string[];
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const api = useApi();
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [grants, setGrants] = useState<NamespaceGrant[]>(
    role?.namespaces?.length
      ? role.namespaces.map((g) => ({ namespace: g.namespace, operations: g.operations ?? [] }))
      : [{ namespace: "", operations: [] }]
  );
  const [global, setGlobal] = useState<string[]>(role?.global ?? []);
  const [configs, setConfigs] = useState<ConfigGrant[]>(
    role?.configs?.length ? role.configs.map((c) => ({ path: c.path, operations: c.operations ?? [] })) : []
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const nsOps = operations.filter((o) => !GLOBAL_OPS.includes(o) && !CONFIG_OPS.includes(o));
  const configOps = CONFIG_OPS.filter((o) => operations.includes(o));

  // Fetched here rather than with the list of roles: it walks the whole
  // repository, and both tabs of Access management would have paid for it on
  // every load. Failing is harmless — the field takes free text either way.
  const [configPrefix, setConfigPrefix] = useState("");
  const [configPaths, setConfigPaths] = useState<string[]>([]);
  useEffect(() => {
    if (configOps.length === 0) return;
    let live = true;
    api
      .configPaths()
      .then((r) => {
        if (!live) return;
        setConfigPrefix(r.root);
        setConfigPaths(r.paths ?? []);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [configOps.length]);

  const setGrant = (i: number, patch: Partial<NamespaceGrant>) =>
    setGrants((gs) => gs.map((g, idx) => (idx === i ? { ...g, ...patch } : g)));

  const toggleOp = (i: number, op: string) =>
    setGrants((gs) =>
      gs.map((g, idx) => {
        if (idx !== i) return g;
        const has = g.operations.includes(op);
        return {
          ...g,
          operations: has ? g.operations.filter((o) => o !== op) : [...g.operations, op],
        };
      })
    );

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      // Rows left blank are dropped rather than saved as a grant on no namespace.
      const cleaned = grants
        .map((g) => ({ namespace: g.namespace.trim(), operations: g.operations }))
        .filter((g) => g.namespace !== "");
      await api.saveRole(name.trim(), {
        description: description.trim(),
        namespaces: cleaned,
        global,
        // Rows left blank are dropped, as for namespaces.
        configs: configs
          .map((c) => ({ path: c.path.trim(), operations: c.operations }))
          .filter((c) => c.path !== ""),
      });
      onSaved(name.trim());
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const wouldReplace = !role && existingNames.includes(name.trim());

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="modal-title">{role ? role.name : "New role"}</div>
        </div>

        <div className="modal-body pad">
          {error && <div className="alert">{error}</div>}

          {!role && (
            <div className="field">
              <label className="muted">Name</label>
              <input
                className="input"
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="payments-developer"
              />
              {wouldReplace && (
                <p className="muted field-hint">A role with this name exists and will be replaced.</p>
              )}
            </div>
          )}

          <div className="field">
            <label className="muted">Description</label>
            <input
              className="input"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="what this role is for"
            />
          </div>

          <div className="field">
            <label className="muted">Namespaces</label>
          </div>
          <p className="muted field-hint">
            Tick what the role may do in each namespace. Reading a secret's values is separate from
            listing its names, so a role can see that a secret exists without seeing what is in it.
            Use <code>*</code> as the namespace to mean every one of them.
          </p>

          <datalist id="role-ns-options">
            {namespaceOptions.map((n) => (
              <option key={n} value={n} />
            ))}
          </datalist>

          {grants.map((g, i) => (
            <div key={i} className="grant-editor">
              <div className="row grant-editor-head">
                <input
                  className="input sm"
                  list="role-ns-options"
                  value={g.namespace}
                  onChange={(e) => setGrant(i, { namespace: e.target.value })}
                  placeholder="namespace, or *"
                />
                <label
                  className="check"
                  title="Every operation, including ones added in future versions"
                >
                  <input
                    type="checkbox"
                    checked={g.operations.includes("*")}
                    onChange={(e) => setGrant(i, { operations: e.target.checked ? ["*"] : [] })}
                  />
                  <span>everything</span>
                </label>
                <button
                  className="btn danger sm"
                  onClick={() => setGrants((gs) => gs.filter((_, idx) => idx !== i))}
                  disabled={grants.length === 1}
                >
                  Remove
                </button>
              </div>

              {!g.operations.includes("*") && (
                <div className="op-grid">
                  {nsOps.map((op) => (
                    <label key={op} className="check">
                      <input
                        type="checkbox"
                        checked={g.operations.includes(op)}
                        onChange={() => toggleOp(i, op)}
                      />
                      <span>{op}</span>
                    </label>
                  ))}
                </div>
              )}
            </div>
          ))}

          <button
            className="btn ghost sm"
            onClick={() => setGrants((gs) => [...gs, { namespace: "", operations: [] }])}
          >
            + Add namespace
          </button>

          {configOps.length > 0 && (
            <>
              <div className="field">
                <label className="muted">Configurations</label>
              </div>
              <p className="muted field-hint">
                Access to service configuration, granted by path. A grant covers the path and
                everything under it — and only that path: <code>abs</code> does not include{" "}
                <code>abs-plus</code>. Use <code>*</code> for all of them.
              </p>
              {/* The prefix is where the portal looks, and paths are written
                  relative to it. Repeating it in a grant produces one that
                  matches nothing, with no error anywhere — worth a line here
                  rather than an afternoon of looking at an empty page. */}
              {configPrefix && (
                <p className="muted field-hint">
                  Relative to <code>{configPrefix}</code>, which the deployment already
                  supplies — write <code>abs</code>, not <code>{configPrefix}abs</code>.
                </p>
              )}

              {configs.map((c, i) => (
                <div key={i} className="row grant-editor-head">
                  <input
                    className="input sm"
                    value={c.path}
                    onChange={(e) =>
                      setConfigs((cs) => cs.map((x, idx) => (idx === i ? { ...x, path: e.target.value } : x)))
                    }
                    placeholder="path, or *"
                    list={configPaths.length ? "config-paths" : undefined}
                  />
                  {configOps.map((op) => (
                    <label key={op} className="check">
                      <input
                        type="checkbox"
                        checked={c.operations.includes(op)}
                        onChange={() =>
                          setConfigs((cs) =>
                            cs.map((x, idx) =>
                              idx === i
                                ? {
                                    ...x,
                                    operations: x.operations.includes(op)
                                      ? x.operations.filter((o) => o !== op)
                                      : [...x.operations, op],
                                  }
                                : x
                            )
                          )
                        }
                      />
                      <span>{CONFIG_OP_LABELS[op] ?? op}</span>
                    </label>
                  ))}
                  <button
                    className="btn danger sm"
                    onClick={() => setConfigs((cs) => cs.filter((_, idx) => idx !== i))}
                  >
                    Remove
                  </button>
                </div>
              ))}

              <button
                className="btn ghost sm"
                onClick={() => setConfigs((cs) => [...cs, { path: "", operations: [] }])}
              >
                + Add configuration path
              </button>

              {/* Suggestions only — the field stays free text, so a path that
                  does not exist yet can still be granted. */}
              {configPaths.length > 0 && (
                <datalist id="config-paths">
                  {configPaths.map((p) => (
                    <option key={p} value={p} />
                  ))}
                </datalist>
              )}
            </>
          )}

          <div className="field roles-global">
            <label className="muted">Portal administration</label>
          </div>
          <p className="muted field-hint">
            Not tied to a namespace. Granting <code>*</code> above does not include these —
            reaching every namespace and administering the portal are different powers.
          </p>
          <div className="op-grid">
            {GLOBAL_OPS.filter((o) => operations.includes(o)).map((op) => (
              <label key={op} className="check">
                <input
                  type="checkbox"
                  checked={global.includes(op)}
                  onChange={() =>
                    setGlobal((gs) => (gs.includes(op) ? gs.filter((o) => o !== op) : [...gs, op]))
                  }
                />
                <span>{GLOBAL_OP_LABELS[op] ?? op}</span>
              </label>
            ))}
          </div>

          <div className="modal-actions">
            <button className="btn ghost" onClick={onClose} disabled={busy}>
              Cancel
            </button>
            <button className="btn" onClick={save} disabled={busy || !name.trim()}>
              {busy ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
