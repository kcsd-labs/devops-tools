import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useApi } from "../useApi";
import type { Me } from "../api";

export function Namespaces() {
  const api = useApi();
  const nav = useNavigate();
  const [list, setList] = useState<string[]>([]);
  const [wildcard, setWildcard] = useState(false);
  const [loaded, setLoaded] = useState(false); // tells "still loading" apart from "nothing to show"
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState("");
  const [manual, setManual] = useState("");
  const [search, setSearch] = useState("");

  // Keeping the client in a ref stops a silently renewed token from reloading
  // the list, which showed up as the grid flickering every few minutes.
  const apiRef = useRef(api);
  apiRef.current = api;

  useEffect(() => {
    apiRef.current
      .namespaces()
      .then((r) => {
        setList(r.namespaces);
        setWildcard(r.wildcard);
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoaded(true));
    apiRef.current
      .me()
      .then(setMe)
      .catch(() => {});
  }, []);

  // Loaded successfully, but nothing came back: the roles grant no namespaces.
  const noAccess = loaded && !error && !wildcard && list.length === 0;

  // Cluster-wide access with an empty list means the namespaces could not be
  // read — the access is still there, so fall back to opening one by name.
  const blindWildcard = loaded && !error && wildcard && list.length === 0;

  const shown = search ? list.filter((ns) => ns.toLowerCase().includes(search.toLowerCase())) : list;
  // A wildcard role may open namespaces the list does not have: ones hidden by
  // configuration, or created since the page loaded. Rather than keeping a
  // second input around for that, offer it exactly when the filter finds
  // nothing — which is when someone is looking for a name that is not there.
  const offerDirect = wildcard && search.trim() !== "" && shown.length === 0;

  return (
    <div className="page">
      <div className="page-head">
        <h1 className="page-title">Namespaces</h1>
        {list.length > 0 && (
          <input
            className="input sm"
            placeholder={wildcard ? "Filter or type a namespace…" : "Filter namespaces…"}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && offerDirect && nav(`/ns/${search.trim()}/pods`)}
          />
        )}
      </div>

      {error && <div className="alert">{error}</div>}

      {noAccess ? (
        <div className="card">
          <div className="card-title">No access</div>
          <p className="muted">
            {me && (me.roles?.length ?? 0) > 0
              ? "Your roles do not grant access to any namespace in this environment."
              : "No roles are assigned to your account."}{" "}
            Ask your platform team to grant access.
          </p>
          {me && (
            <div className="muted noaccess-id">
              <div>
                Signed in as <b>{me.username}</b>
                {me.email ? ` (${me.email})` : ""}
              </div>
              <div>Roles: {me.roles?.length ? me.roles.join(", ") : "—"}</div>
              <div>Environment: {me.environment}</div>
            </div>
          )}
        </div>
      ) : blindWildcard ? (
        <div className="card">
          <div className="card-title">Access to all namespaces</div>
          <p className="muted">
            The list of namespaces could not be read from the cluster, so enter the one you want to
            open:
          </p>
          <div className="row">
            <input
              className="input"
              placeholder="for example, payments-dev"
              value={manual}
              onChange={(e) => setManual(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && manual && nav(`/ns/${manual}/pods`)}
            />
            <button className="btn" disabled={!manual} onClick={() => nav(`/ns/${manual}/pods`)}>
              Open
            </button>
          </div>
        </div>
      ) : (
        <div className="grid">
          {shown.map((ns) => (
            <button key={ns} className="card card-clickable" onClick={() => nav(`/ns/${ns}/pods`)}>
              <div className="card-title">{ns}</div>
              <div className="muted">Pods and Helm releases →</div>
            </button>
          ))}
          {!loaded && !error && <div className="muted">Loading…</div>}
          {list.length > 0 && shown.length === 0 && !offerDirect && (
            <div className="muted">No namespaces match.</div>
          )}
          {offerDirect && (
            <button className="card card-clickable" onClick={() => nav(`/ns/${search.trim()}/pods`)}>
              <div className="card-title">{search.trim()}</div>
              <div className="muted">Not in the list — open anyway →</div>
            </button>
          )}
        </div>
      )}
    </div>
  );
}
