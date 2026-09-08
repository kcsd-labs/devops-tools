import { useState } from "react";
import { useSession, useOidcSignIn } from "../session";

// The sign-in screen, for every provider.
//
// With local or ldap the form is the only way in and is shown directly. With an
// identity provider it is the button that everyone uses, and the form belongs to
// one account: the one configured under auth.bootstrapAdmins.passwordLogin, for
// before anyone has been granted access and for when the provider cannot be
// reached. It is therefore behind a link — present, so nobody has to know about
// it in advance, but not competing with the route that is almost always right.
export function Login({ showFormFirst = false }: { showFormFirst?: boolean }) {
  const { provider, signIn, expired, passwordLogin, brand, providerName } = useSession();
  const oidcSignIn = useOidcSignIn();

  const isOidc = provider === "oidc";
  // With local or ldap there is nothing to reveal: the form is the screen.
  const [formOpen, setFormOpen] = useState(!isOidc || showFormFirst);

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!signIn) return;
    setBusy(true);
    setError("");
    try {
      await signIn(username, password);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="centered">
      <form className="login-card" onSubmit={submit}>
        <div className="login-brand">
          <div className="brand-logo">{brand.initials}</div>
          <div>
            <div className="brand-title">{brand.name}</div>
            <div className="brand-version">Kubernetes self-service</div>
          </div>
        </div>

        {expired && !error && (
          <div className="alert login-alert">Your session expired. Please sign in again.</div>
        )}
        {error && <div className="alert login-alert">{error}</div>}

        {/* The provider comes first under oidc: it is how everyone signs in. */}
        {isOidc && oidcSignIn && (
          <button type="button" className="btn login-submit" onClick={() => oidcSignIn()}>
            Sign in with {providerName}
          </button>
        )}

        {formOpen && (
          <>
            {isOidc && <div className="login-sep" />}

            <label className="login-label" htmlFor="username">
              Username
            </label>
            <input
              id="username"
              className="input"
              autoFocus
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />

            <label className="login-label" htmlFor="password">
              Password
            </label>
            <input
              id="password"
              className="input"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />

            <button
              className={"btn login-submit" + (isOidc ? " ghost" : "")}
              type="submit"
              disabled={busy || !username || !password}
            >
              {busy ? "Signing in…" : "Sign in"}
            </button>
          </>
        )}

        {/* Only where there is an account to sign in as. */}
        {isOidc && passwordLogin && !formOpen && (
          <button type="button" className="link-btn login-alt" onClick={() => setFormOpen(true)}>
            Administrator sign-in
          </button>
        )}

        {!isOidc && (
          <div className="muted login-hint">
            {provider === "ldap"
              ? "Authenticating against the corporate directory"
              : "Local account"}
          </div>
        )}
      </form>
    </div>
  );
}
