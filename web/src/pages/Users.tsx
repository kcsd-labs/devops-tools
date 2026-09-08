import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useApi } from "../useApi";
import { useScrollLock } from "../useScrollLock";
import { useDialogs } from "../components/Dialogs";
import { AccessTabs } from "../components/AccessTabs";
import type { Me, PortalUser, Role } from "../api";

// Who can use the portal, and what they may do.
//
// With a directory, people appear here the first time they sign in — there is
// nobody to invite, and an account with no roles is the normal starting state
// rather than a fault. With the local provider there is no directory to appear
// from, so accounts are created here instead.
export function Users() {
  const api = useApi();
  const [users, setUsers] = useState<PortalUser[]>([]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [me, setMe] = useState<Me | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  // Arriving from the roles list carries the role in the address, so "who holds
  // this" lands on the people rather than on an unfiltered table.
  const [params] = useSearchParams();
  const [search, setSearch] = useState(() => params.get("role") ?? "");

  const [creating, setCreating] = useState(false);
  const [inviting, setInviting] = useState(false);
  const [editing, setEditing] = useState<PortalUser | null>(null);

  // Handing the same role to several people is the common case, and doing it
  // one dialog at a time is the reason it gets put off. Selection lives here
  // rather than in the role editor: what a role permits and who holds it are
  // different decisions, and one screen that does both is one people change by
  // mistake.
  const [selectMode, setSelectMode] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [granting, setGranting] = useState<{ add: boolean } | null>(null);
  const [busy, setBusy] = useState(false);

  const { dialogs, confirm, alertDlg, toast } = useDialogs();

  // As elsewhere: a silently renewed token must not reload the table.
  const apiRef = useRef(api);
  apiRef.current = api;

  const load = useCallback(() => {
    Promise.all([apiRef.current.users(), apiRef.current.roles(), apiRef.current.me()])
      .then(([u, r, m]) => {
        setUsers(u);
        setRoles(r.roles);
        setMe(m);
        setError("");
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoaded(true));
  }, []);

  useEffect(load, [load]);
  useScrollLock(creating || !!editing);

  const canManage = me?.capabilities?.manageUsers ?? false;
  const isLocal = me?.authProvider === "local";

  const q = search.trim().toLowerCase();
  const shown = q
    ? users.filter(
        (u) =>
          u.username.toLowerCase().includes(q) ||
          (u.email ?? "").toLowerCase().includes(q) ||
          u.roles.some((r) => r.toLowerCase().includes(q)) ||
          u.groups.some((g) => g.toLowerCase().includes(q))
      )
    : users;

  const withoutAccess = users.filter((u) => u.roles.length === 0).length;

  const toggle = (name: string) =>
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });

  const allShownSelected = shown.length > 0 && shown.every((u) => selected.has(u.username));
  const toggleAll = () =>
    setSelected((s) => {
      const next = new Set(s);
      if (allShownSelected) shown.forEach((u) => next.delete(u.username));
      else shown.forEach((u) => next.add(u.username));
      return next;
    });

  // One role across the selection, added or taken away. Never a replacement of
  // anybody's whole set: if the wrong people were selected, undoing this is
  // doing the opposite, and undoing a replacement is impossible.
  const applyRole = async (role: string, add: boolean) => {
    const names = [...selected];
    if (!names.length) return;

    // Taking a role away from yourself is allowed — the way back in is the
    // break-glass account — but it should not happen by accident on the way to
    // changing somebody else.
    if (!add && me && names.includes(me.username) && me.roles?.includes(role)) {
      const ok = await confirm({
        title: "This takes the role away from you as well",
        message:
          `You are in the selection, and ${role} is one of your own roles. ` +
          "If it is what grants you access to this page, you will lose it.",
        okText: "Take it away anyway",
        danger: true,
      });
      if (!ok) return;
    }

    setBusy(true);
    try {
      const r = await api.setRoleMembers(role, names, add);
      const verb = add ? "granted to" : "taken from";
      const parts = [`${role} ${verb} ${r.changed.length} of ${names.length}`];
      if (r.unchanged.length) parts.push(`${r.unchanged.length} already as asked`);
      if (r.unknown.length) parts.push(`not found: ${r.unknown.join(", ")}`);
      toast(parts.join(" · "));

      // Revoking a role the deployment itself grants changes the store and
      // nothing else: bootstrap roles are added back on every request. Saying
      // "done" here would leave someone believing an administrator had been
      // demoted.
      if (r.stillGranted.length) {
        await alertDlg(
          `${r.stillGranted.join(", ")} still hold ${role}: it is granted by ` +
            "auth.bootstrapAdmins in the deployment, which this page cannot change. " +
            "Edit the values and redeploy to remove it.",
          "Still granted by the deployment"
        );
      }
      setGranting(null);
      setSelectMode(false);
      setSelected(new Set());
      load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  // What deletion actually does depends on where the account came from, and the
  // difference matters enough to spell out: for an account this portal owns it
  // is final, and for anyone else it is a revocation they will discover the
  // next time they sign in.
  const removalMessage = (u: PortalUser): string => {
    let text: string;
    if (u.invited) {
      text = "The invitation is withdrawn. Nothing has been claimed yet, so nobody loses anything.";
    } else if (u.managed) {
      text = "The account is removed and can no longer sign in. This cannot be undone.";
    } else {
      text =
        `${u.username} signs in through ${u.provider || "an identity provider"}, so this does not ` +
        "stop them signing in — it takes away their roles and forgets they were ever here. " +
        "They will reappear, with no access, the next time they open the portal.";
    }
    if (u.bootstrap) {
      // Said here rather than afterwards: a warning that arrives once the
      // decision is made is a warning nobody can act on.
      text +=
        "\n\nThey are listed in auth.bootstrapAdmins, so the roles that grants come back with " +
        "them. Taking those away needs a change to the deployment, not this button.";
    }
    return text;
  };

  const remove = async (u: PortalUser) => {
    const ok = await confirm({
      title: `Delete ${u.username}?`,
      message: removalMessage(u),
      okText: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteUser(u.username);
      toast(`${u.username} deleted`);
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
        <div className="row">
          {users.length > 0 && (
            <input
              className="input sm"
              placeholder="Filter by name, group or role…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          )}
          {canManage && users.length > 0 && (
            selectMode ? (
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
            )
          )}
          {/* Only with the local provider: anywhere else the credentials belong
              to the directory, and an account created here could never sign in. */}
          {/* Under any provider, unlike creating an account: an invitation has
              no password in it — it is roles waiting for a name to turn up. */}
          {canManage && (
            <button className="btn ghost" onClick={() => setInviting(true)}>
              + Invite
            </button>
          )}
          {canManage && isLocal && (
            <button className="btn" onClick={() => setCreating(true)}>
              + New user
            </button>
          )}
        </div>
      </div>

      {error && <div className="alert">{error}</div>}
      {!loaded && !error && <div className="muted">Loading…</div>}

      {loaded && !error && users.length === 0 && (
        <div className="card">
          <div className="card-title">No accounts yet</div>
          <p className="muted">
            {isLocal
              ? "Create one to get started."
              : "People appear here the first time they sign in. Ask them to open the portal once, then grant them access."}
          </p>
        </div>
      )}

      {selectMode && (
        <div className="bulk-bar">
          <span>Selected: {selected.size}</span>
          <div className="row">
            <button
              className="btn sm"
              disabled={!selected.size || busy}
              onClick={() => setGranting({ add: true })}
            >
              Grant a role
            </button>
            <button
              className="btn ghost sm"
              disabled={!selected.size || busy}
              onClick={() => setGranting({ add: false })}
            >
              Take a role away
            </button>
          </div>
        </div>
      )}

      {users.length > 0 && (
        <>
          <p className="muted">
            {users.length} {users.length === 1 ? "account" : "accounts"}
            {withoutAccess > 0 && `, ${withoutAccess} without access`}.
          </p>

          <div className="card no-pad">
            <table className="table">
              <thead>
                <tr>
                  {selectMode && (
                    <th className="check-col">
                      <input type="checkbox" checked={allShownSelected} onChange={toggleAll} />
                    </th>
                  )}
                  <th>User</th>
                  <th>Roles</th>
                  <th>Groups</th>
                  <th>Signed in</th>
                  {canManage && <th className="right">Actions</th>}
                </tr>
              </thead>
              <tbody>
                {shown.map((u) => (
                  <tr key={u.username} className={selectMode && selected.has(u.username) ? "row-selected" : ""}>
                    {selectMode && (
                      <td className="check-col">
                        <input
                          type="checkbox"
                          checked={selected.has(u.username)}
                          onChange={() => toggle(u.username)}
                        />
                      </td>
                    )}
                    <td>
                      <div className="user-name">{u.username}</div>
                      <div className="muted user-sub">
                        {u.email ? `${u.email} · ` : ""}
                        {u.invited ? (
                          <span
                            className="chip-invited"
                            title="Roles prepared for this name. They apply the first time somebody signs in under it — check the spelling if it stays here."
                          >
                            invited
                          </span>
                        ) : (
                          u.provider
                        )}
                      </div>
                    </td>
                    <td>
                      {u.roles.length ? (
                        <div className="role-chips inline">
                          {u.roles.map((r) => (
                            <span key={r} className="role-chip">
                              {r}
                            </span>
                          ))}
                          {u.bootstrap && (
                            <span
                              className="role-chip chip-fixed"
                              title="Granted by auth.bootstrapAdmins in the deployment; cannot be changed here"
                            >
                              fixed
                            </span>
                          )}
                        </div>
                      ) : (
                        <span className="muted">no access</span>
                      )}
                    </td>
                    <td className="group-col">
                      <GroupCell groups={u.groups} />
                    </td>
                    <td className="muted nowrap">
                      {u.invited ? (
                        <div title={`Invited ${fmt(u.firstSeen)}`}>never</div>
                      ) : (
                        <div title={`First seen ${fmt(u.firstSeen)}`}>{lastSeen(u)}</div>
                      )}
                    </td>
                    {canManage && (
                      <td className="right nowrap">
                        <button className="btn ghost sm" onClick={() => setEditing(u)}>
                          Edit
                        </button>
                        <button className="btn danger sm" onClick={() => remove(u)}>
                          Delete
                        </button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
            {shown.length === 0 && <div className="empty-row muted">Nobody matches.</div>}
          </div>
        </>
      )}

      {granting && (
        <RolePickerDialog
          roles={roles}
          add={granting.add}
          count={selected.size}
          busy={busy}
          onClose={() => setGranting(null)}
          onPick={(role) => void applyRole(role, granting.add)}
        />
      )}

      {inviting && (
        <InviteDialog
          roles={roles}
          onClose={() => setInviting(false)}
          onSaved={(name) => {
            setInviting(false);
            toast(`${name} invited`);
            load();
          }}
        />
      )}

      {creating && (
        <CreateUserDialog
          roles={roles}
          onClose={() => setCreating(false)}
          onSaved={(name) => {
            setCreating(false);
            toast(`${name} created`);
            load();
          }}
        />
      )}

      {editing && (
        <EditUserDialog
          user={editing}
          roles={roles}
          onClose={() => setEditing(null)}
          onSaved={(msg) => {
            setEditing(null);
            toast(msg);
            load();
          }}
        />
      )}
    </div>
  );
}

// How many groups a row shows before the rest are folded away. Enough to
// recognise somebody by their team; short of the twenty-odd a real directory
// hands back for anyone who has been at a company a while.
const GROUPS_SHOWN = 4;

// A directory answers with distinguished names —
//
//   CN=payments-read,OU=Groups,OU=Departments,DC=example,DC=com
//
// where all that identifies the group is the first part; the rest says where it
// lives in the tree, which is the same for nearly every one of them. The column
// exists to be read at a glance while deciding what to grant, so it shows the
// name and keeps the full DN in the tooltip.
function groupLabel(group: string): string {
  const [first] = group.split(",");
  const eq = first.indexOf("=");
  return (eq === -1 ? first : first.slice(eq + 1)).trim();
}

function GroupCell({ groups }: { groups: string[] }) {
  const [expanded, setExpanded] = useState(false);
  if (!groups.length) return <span className="muted">—</span>;

  const shown = expanded ? groups : groups.slice(0, GROUPS_SHOWN);
  const hidden = groups.length - shown.length;
  return (
    <div className="group-chips">
      {shown.map((g) => (
        <span key={g} className="group-chip" title={g}>
          {groupLabel(g)}
        </span>
      ))}
      {(hidden > 0 || expanded) && (
        <button type="button" className="link-btn" onClick={() => setExpanded(!expanded)}>
          {expanded ? "show fewer" : `+${hidden} more`}
        </button>
      )}
    </div>
  );
}

// --- dialogs ---------------------------------------------------------------

function RolePicker({
  roles,
  selected,
  onChange,
}: {
  roles: Role[];
  selected: string[];
  onChange: (roles: string[]) => void;
}) {
  const toggle = (name: string) =>
    onChange(selected.includes(name) ? selected.filter((r) => r !== name) : [...selected, name]);

  if (roles.length === 0) {
    return <p className="muted">No roles are defined yet. Create one under Roles first.</p>;
  }
  return (
    <div className="role-picker">
      {roles.map((r) => (
        <label key={r.name} className="check role-option">
          <input
            type="checkbox"
            checked={selected.includes(r.name)}
            onChange={() => toggle(r.name)}
          />
          <span>
            <span className="role-option-name">{r.name}</span>
            {r.description && <span className="muted role-option-desc">{r.description}</span>}
          </span>
        </label>
      ))}
    </div>
  );
}

function CreateUserDialog({
  roles,
  onClose,
  onSaved,
}: {
  roles: Role[];
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const api = useApi();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      await api.createUser({
        username: username.trim(),
        email: email.trim(),
        password,
        roles: picked,
      });
      onSaved(username.trim());
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="modal-title">New user</div>
        </div>

        <div className="modal-body pad">
          {error && <div className="alert">{error}</div>}

          <div className="field">
            <label className="muted">Username</label>
            <input
              className="input"
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="ivan.petrov"
            />
          </div>

          <div className="field">
            <label className="muted">Email (optional)</label>
            <input
              className="input"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="ivan@example.com"
            />
          </div>

          <div className="field">
            <label className="muted">Password</label>
            <input
              className="input"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="at least 8 characters"
            />
          </div>
          <p className="muted field-hint">
            Give it to the person over a channel you trust. It is stored only as a hash and cannot
            be read back — if it is lost, set a new one.
          </p>

          <div className="field">
            <label className="muted">Roles</label>
            <RolePicker roles={roles} selected={picked} onChange={setPicked} />
          </div>
          {picked.length === 0 && (
            <p className="muted field-hint">
              With no role the account can sign in but reach nothing. That is a valid starting point.
            </p>
          )}

          <div className="modal-actions">
            <button className="btn ghost" onClick={onClose} disabled={busy}>
              Cancel
            </button>
            <button
              className="btn"
              onClick={save}
              disabled={busy || !username.trim() || password.length < 8}
            >
              {busy ? "Creating…" : "Create"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function EditUserDialog({
  user,
  roles,
  onClose,
  onSaved,
}: {
  user: PortalUser;
  roles: Role[];
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const api = useApi();
  const [picked, setPicked] = useState<string[]>(user.roles);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const changes: string[] = [];
      if (!sameSet(picked, user.roles)) {
        await api.setUserRoles(user.username, picked);
        changes.push("roles updated");
      }
      if (password) {
        await api.setUserPassword(user.username, password);
        changes.push("password changed");
      }
      onSaved(changes.length ? `${user.username}: ${changes.join(", ")}` : "Nothing to change");
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="modal-title">{user.username}</div>
          <span className="muted">
            {user.email ? `${user.email} · ` : ""}
            {user.provider}
          </span>
        </div>

        <div className="modal-body pad">
          {error && <div className="alert">{error}</div>}

          <div className="field">
            <label className="muted">Roles</label>
            <RolePicker roles={roles} selected={picked} onChange={setPicked} />
          </div>

          {user.bootstrap && (
            <p className="muted field-hint">
              This account is also listed under <code>auth.bootstrapAdmins</code>, so it keeps those
              roles whatever is unticked here. That is the way back in if access is misconfigured.
            </p>
          )}
          {picked.length === 0 && !user.bootstrap && (
            <p className="muted field-hint">
              Unticking everything revokes access immediately, including for a session already open.
            </p>
          )}

          {/* Only an account created here has a password to change; a directory
              account authenticates somewhere else entirely. */}
          {user.managed && (
            <div className="field">
              <label className="muted">New password (optional)</label>
              <input
                className="input"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="leave empty to keep the current one"
              />
            </div>
          )}

          <div className="modal-actions">
            <button className="btn ghost" onClick={onClose} disabled={busy}>
              Cancel
            </button>
            <button
              className="btn"
              onClick={save}
              disabled={busy || (password.length > 0 && password.length < 8)}
            >
              {busy ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// --- helpers ---------------------------------------------------------------

function sameSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(b);
  return a.every((x) => set.has(x));
}

// An account created here but never used has no last-seen time. Saying so beats
// printing the zero date.
function lastSeen(u: PortalUser): string {
  if (!u.lastSeen || u.lastSeen.startsWith("0001")) return "never";
  return fmt(u.lastSeen);
}

function fmt(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

// Which role to hand to, or take from, the people selected.
//
// Deliberately one role at a time. A dialog that set several at once would
// invite "give these five everything", and the resulting grant is the kind
// nobody remembers making.
function RolePickerDialog({
  roles,
  add,
  count,
  busy,
  onClose,
  onPick,
}: {
  roles: Role[];
  add: boolean;
  count: number;
  busy: boolean;
  onClose: () => void;
  onPick: (role: string) => void;
}) {
  const [role, setRole] = useState(roles[0]?.name ?? "");
  useScrollLock(true);

  const chosen = roles.find((r) => r.name === role);

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="modal-title">
            {add ? "Grant a role" : "Take a role away"} — {count}{" "}
            {count === 1 ? "person" : "people"}
          </div>
          <button className="btn ghost sm" onClick={onClose}>
            ✕
          </button>
        </div>
        <div className="modal-body pad">
          {roles.length === 0 ? (
            <p className="muted">No roles exist yet. Create one under Roles first.</p>
          ) : (
            <>
              <div className="field">
                <label className="muted">Role</label>
                <select className="input" value={role} onChange={(e) => setRole(e.target.value)}>
                  {roles.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </select>
              </div>
              {chosen?.description && <p className="muted field-hint">{chosen.description}</p>}
              <p className="muted field-hint">
                {add
                  ? "Their other roles are left alone — this adds one."
                  : "Only this role is removed; anything else they hold stays."}
              </p>
            </>
          )}

          <div className="modal-actions">
            <button className="btn ghost" onClick={onClose} disabled={busy}>
              Cancel
            </button>
            <button
              className={add ? "btn" : "btn danger"}
              disabled={!role || busy}
              onClick={() => onPick(role)}
            >
              {busy ? "Applying…" : add ? "Grant" : "Take away"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// Roles prepared for somebody who has not signed in yet.
//
// The name has to be the one their provider will send — under OIDC that is the
// preferred_username claim, which is usually but not always what people call
// each other. Nothing checks it: a wrong name simply never matches, which is
// why the list marks an unclaimed invitation rather than letting it blend in.
function InviteDialog({
  roles,
  onClose,
  onSaved,
}: {
  roles: Role[];
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const api = useApi();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useScrollLock(true);

  const toggleRole = (name: string) =>
    setPicked((p) => (p.includes(name) ? p.filter((r) => r !== name) : [...p, name]));

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await api.invite({ username: username.trim(), email: email.trim(), roles: picked });
      onSaved(username.trim());
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="modal-title">Invite</div>
          <button className="btn ghost sm" onClick={onClose}>
            ✕
          </button>
        </div>
        <div className="modal-body pad">
          {error && <div className="alert">{error}</div>}

          <div className="field">
            <label className="muted">Username</label>
            <input
              className="input"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="as their provider will send it"
              autoFocus
            />
          </div>
          <p className="muted field-hint">
            It has to match exactly, apart from capitalisation. Nothing here can check it —
            a name that never matches simply waits, marked as invited, until somebody
            notices and removes it.
          </p>

          <div className="field">
            <label className="muted">Email (optional)</label>
            <input
              className="input"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="shown in the list until they sign in"
            />
          </div>

          <div className="field">
            <label className="muted">Roles</label>
          </div>
          {roles.length === 0 ? (
            <p className="muted field-hint">No roles exist yet. Create one under Roles first.</p>
          ) : (
            <div className="op-grid">
              {roles.map((r) => (
                <label key={r.name} className="check">
                  <input
                    type="checkbox"
                    checked={picked.includes(r.name)}
                    onChange={() => toggleRole(r.name)}
                  />
                  <span>{r.name}</span>
                </label>
              ))}
            </div>
          )}
          <p className="muted field-hint">
            At least one: an invitation with no roles would grant nothing when it is claimed.
          </p>

          <div className="modal-actions">
            <button className="btn ghost" onClick={onClose} disabled={busy}>
              Cancel
            </button>
            <button
              className="btn"
              disabled={!username.trim() || picked.length === 0 || busy}
              onClick={submit}
            >
              {busy ? "Inviting…" : "Invite"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
