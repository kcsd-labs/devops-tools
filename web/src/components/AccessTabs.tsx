import { NavLink, useLocation } from "react-router-dom";

// Switches between the two halves of access management: the people, and what
// the roles they hold actually permit.
//
// They are one screen rather than two menu entries because neither answers a
// question on its own — granting access means picking a role, and changing a
// role means knowing who holds it. Links rather than local state, so a tab can
// be linked to and the browser's back button behaves.
export function AccessTabs() {
  const { pathname } = useLocation();
  const on = (p: string) => "seg-btn" + (pathname.startsWith(p) ? " on" : "");

  return (
    <div className="seg" role="tablist">
      <NavLink to="/access/users" className={() => on("/access/users")}>
        Users
      </NavLink>
      <NavLink to="/access/roles" className={() => on("/access/roles")}>
        Roles
      </NavLink>
    </div>
  );
}
