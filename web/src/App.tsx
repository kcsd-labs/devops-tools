import { Suspense, lazy } from "react";
import { Routes, Route, Navigate, useLocation } from "react-router-dom";
import { Layout } from "./components/Layout";
import { Namespaces } from "./pages/Namespaces";
import { Pods } from "./pages/Pods";
import { PodDetail } from "./pages/PodDetail";
import { Helm } from "./pages/Helm";
import { Secrets } from "./pages/Secrets";
import { Users } from "./pages/Users";
import { Roles } from "./pages/Roles";
import { MultiLogs } from "./pages/MultiLogs";
import { Login } from "./pages/Login";
import { useSession } from "./session";

// An address that opens the sign-in screen with the credentials form already
// showing. Worth keeping alongside the link on the screen itself: it is the one
// thing to put in a runbook for when the identity provider is down.
const LOCAL_SIGNIN_PATH = "/signin/local";

// Loaded on demand. It brings the code editor with it, which is the largest
// thing in the bundle by some way, and most installations have configuration
// management switched off entirely — they should not pay for it on every load.
const Configurations = lazy(() =>
  import("./pages/Configurations").then((m) => ({ default: m.Configurations }))
);

export default function App() {
  const session = useSession();
  const { pathname } = useLocation();

  // No automatic redirect to the provider, deliberately. It saves a click, and
  // costs the whole page when the provider is unreachable: the browser lands on
  // its own error and there is no telling whether the portal is up. With a
  // button, the page loads and only the button fails.
  if (!session.ready) return <Centered>Loading…</Centered>;

  if (!session.token) {
    return <Login showFormFirst={pathname === LOCAL_SIGNIN_PATH} />;
  }

  return (
    <Layout>
      <Routes>
        <Route path="/" element={<Namespaces />} />
        <Route path="/ns/:namespace/pods" element={<Pods />} />
        <Route path="/ns/:namespace/pods/:pod" element={<PodDetail />} />
        <Route path="/ns/:namespace/logs" element={<MultiLogs />} />
        <Route path="/ns/:namespace/helm" element={<Helm />} />
        <Route path="/secrets" element={<Secrets />} />
        <Route
          path="/configurations"
          element={
            <Suspense fallback={<div className="page muted">Loading…</div>}>
              <Configurations />
            </Suspense>
          }
        />
        {/* One screen with two tabs, but a route each so a tab can be linked
            to and the back button steps between them. */}
        <Route path="/access" element={<Navigate to="/access/users" replace />} />
        <Route path="/access/users" element={<Users />} />
        <Route path="/access/roles" element={<Roles />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  );
}

function Centered({ children }: { children: React.ReactNode }) {
  return <div className="centered">{children}</div>;
}
