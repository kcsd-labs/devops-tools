import { useEffect, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { useApi } from "../useApi";
import { useSession } from "../session";
import { GlobalToasts } from "../toasts";

export function Layout({ children }: { children: React.ReactNode }) {
  const api = useApi();
  const session = useSession();
  const brand = session.brand;
  const [username, setUsername] = useState("");
  const [roles, setRoles] = useState<string[]>([]);
  const [canSeeUsers, setCanSeeUsers] = useState(false);
  const [canSeeConfigs, setCanSeeConfigs] = useState(false);
  const [version, setVersion] = useState("");
  const [theme, setTheme] = useState<"dark" | "light">(
    () => (localStorage.getItem("devops-tools.theme") as "dark" | "light") ?? "dark"
  );
  const [collapsed, setCollapsed] = useState<boolean>(
    () => localStorage.getItem("devops-tools.sidebarCollapsed") === "1"
  );
  const location = useLocation();

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("devops-tools.theme", theme);
  }, [theme]);

  useEffect(() => {
    localStorage.setItem("devops-tools.sidebarCollapsed", collapsed ? "1" : "0");
  }, [collapsed]);

  // Identity comes from the server rather than the token: the backend is the
  // authority on which roles actually apply.
  useEffect(() => {
    api
      .me()
      .then((m) => {
        setUsername(m.username);
        setRoles(m.roles ?? []);
        setCanSeeUsers(m.capabilities?.users ?? false);
        setCanSeeConfigs(m.capabilities?.configurations ?? false);
        setVersion(m.version ?? "");
      })
      .catch(() => {});
  }, [api]);

  const initials = (username || "?").slice(0, 2).toUpperCase();

  return (
    <div className={"app" + (collapsed ? " collapsed" : "")}>
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-logo">{brand.initials}</div>
          <div>
            <div className="brand-title">{brand.name}</div>
            {/* The build, not the tagline: inside a running instance the useful
                question is which version you are looking at. The tagline is on
                the sign-in screen, where it introduces the product. */}
            <div className="brand-version">{version ? `v${version}` : " "}</div>
          </div>
        </div>

        <nav className="nav nav-main">
          <NavLink to="/" className={() => "nav-item" + (location.pathname === "/" ? " active" : "")}>
            <span>Namespaces</span>
            <span className="chev">›</span>
          </NavLink>
          <NavLink
            to="/secrets"
            className={() => "nav-item" + (location.pathname.startsWith("/secrets") ? " active" : "")}
          >
            <span>Secrets</span>
            <span className="chev">›</span>
          </NavLink>
          {/* Only where the deployment manages configuration and the caller was
              granted a path in it: otherwise the page is an empty list, which
              reads as a fault rather than as an absence of access. */}
          {canSeeConfigs && (
            <NavLink
              to="/configurations"
              className={() =>
                "nav-item" + (location.pathname.startsWith("/configurations") ? " active" : "")
              }
            >
              <span>Configurations</span>
              <span className="chev">›</span>
            </NavLink>
          )}
        </nav>

        {/* Administering the portal sits apart from working in it: everything
            above is about workloads, this is about who may touch them. */}
        {canSeeUsers && (
          <nav className="nav nav-admin">
            <NavLink
              to="/access/users"
              className={() =>
                "nav-item" + (location.pathname.startsWith("/access") ? " active" : "")
              }
            >
              <span>Access management</span>
              <span className="chev">›</span>
            </NavLink>
          </nav>
        )}

        <div className="sidebar-footer">
          <span className="role-label">{roles.length === 1 ? "Role" : "Roles"}:</span>
          {roles.length ? (
            <div className="role-chips">
              {roles.map((r) => (
                <span key={r} className="role-chip" title={r}>
                  {r}
                </span>
              ))}
            </div>
          ) : (
            <span> —</span>
          )}
        </div>
      </aside>

      <main className="main">
        <header className="topbar">
          <div className="topbar-left">
            <button
              className="icon-btn"
              title={collapsed ? "Show sidebar" : "Hide sidebar"}
              onClick={() => setCollapsed((v) => !v)}
            >
              ☰
            </button>
            <div className="topbar-title">{brand.name}</div>
          </div>
          <div className="topbar-actions">
            <button
              className="icon-btn"
              title="Toggle theme"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              {theme === "dark" ? "☾" : "☀"}
            </button>
            <div className="user">
              <div className="avatar">{initials}</div>
              <div className="user-meta">
                <div className="user-name">{username || "—"}</div>
                <button className="link-btn" onClick={session.signOut}>
                  Sign out
                </button>
              </div>
            </div>
          </div>
        </header>

        <div className="content">{children}</div>
      </main>

      <GlobalToasts />
    </div>
  );
}
