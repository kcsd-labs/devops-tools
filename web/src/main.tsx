import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { fetchAuthInfo, SessionRoot, type AuthInfo } from "./session";
import App from "./App";
import "./styles.css";

// The authentication provider is a server-side decision, so the browser asks
// for it before anything is rendered: an OIDC deployment needs a completely
// different provider tree than a password-based one.
function Bootstrap() {
  const [info, setInfo] = useState<AuthInfo | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    fetchAuthInfo()
      .then((i) => {
        setInfo(i);
        // The tab is titled by the same setting as the sidebar, rather than by
        // a name written into index.html that no installation can change.
        if (i.brandName) document.title = i.brandName;
      })
      .catch((e) => setError(e.message));
  }, []);

  if (error) {
    return (
      <div className="centered">
        <div className="session-card">
          <div className="session-title">Cannot reach the server</div>
          <p className="muted">{error}</p>
          <button className="btn" onClick={() => window.location.reload()}>
            Retry
          </button>
        </div>
      </div>
    );
  }
  if (!info) return <div className="centered">Loading…</div>;

  return (
    <SessionRoot info={info}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </SessionRoot>
  );
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Bootstrap />
  </React.StrictMode>
);
