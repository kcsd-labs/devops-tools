import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { AuthProvider, useAuth } from "react-oidc-context";
import { WebStorageStateStore } from "oidc-client-ts";

// DevOps Tools supports two authentication styles and hides the difference behind
// a single session context:
//
//   local / ldap — the backend verifies the credentials and issues a session
//                  token, which we keep in localStorage;
//   oidc         — an external identity provider issues the token and
//                  react-oidc-context owns the redirect flow.
//
// Everything above this layer just asks for `token` and calls `signOut`.

export type AuthInfo = {
  provider: "local" | "ldap" | "oidc";
  /** What this installation calls itself; the technical name stays DevOps Tools. */
  brandName?: string;
  brandInitials?: string;
  oidc?: { issuerUrl: string; clientId: string; displayName?: string };
  /** A password form exists. Under oidc that is the break-glass account. */
  passwordLogin?: boolean;
};

export type Session = {
  provider: AuthInfo["provider"];
  brand: { name: string; initials: string };
  /** What the sign-in button offers to sign in with. Only meaningful for oidc. */
  providerName: string;
  token?: string;
  /** A password form is available. Under oidc that is the break-glass account. */
  passwordLogin: boolean;
  /** True once the provider finished restoring any existing session. */
  ready: boolean;
  /** Set when a previously valid session ended and the user must sign in again. */
  expired: boolean;
  signIn?: (username: string, password: string) => Promise<void>;
  signOut: () => void;
};

const SessionContext = createContext<Session | null>(null);

export function useSession(): Session {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used inside <SessionRoot>");
  return ctx;
}

const TOKEN_KEY = "devops-tools.token";

/**
 * Reads the bearer token from wherever the active provider stores it.
 *
 * Exported as a plain function because the background watchers live outside
 * React and cannot use hooks — they call this on every request so a renewed
 * token is picked up automatically.
 */
export function currentToken(): string | undefined {
  const own = localStorage.getItem(TOKEN_KEY);
  if (own) return own;
  // oidc-client-ts stores the user under "oidc.user:<issuer>:<clientId>"
  const key = Object.keys(localStorage).find((k) => k.startsWith("oidc.user:"));
  if (!key) return undefined;
  try {
    return JSON.parse(localStorage[key] ?? "{}").access_token as string | undefined;
  } catch {
    return undefined;
  }
}

export function authHeaders(): Record<string, string> {
  const token = currentToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/** Fetches the provider configuration before any UI is rendered. */
export async function fetchAuthInfo(): Promise<AuthInfo> {
  const res = await fetch("/api/auth/info", { cache: "no-store" });
  if (!res.ok) throw new Error(`Cannot reach the server (${res.status})`);
  return res.json();
}

/** Chooses the right session implementation for the configured provider. */
/** The displayed name, with the shipped default when none is configured. */
function brandOf(info: AuthInfo): { name: string; initials: string } {
  return { name: info.brandName || "DevOps Tools", initials: info.brandInitials || "DT" };
}

export function SessionRoot({ info, children }: { info: AuthInfo; children: React.ReactNode }) {
  if (info.provider === "oidc") {
    return <OidcSession info={info}>{children}</OidcSession>;
  }
  return <CredentialSession info={info}>{children}</CredentialSession>;
}

/** Posts credentials and returns the token, or throws something readable. */
async function postLogin(username: string, password: string): Promise<string> {
  const res = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) {
    throw new Error(
      res.status === 429
        ? "Too many failed attempts. Try again in a few minutes."
        : "Invalid username or password"
    );
  }
  return (await res.json()).token as string;
}

/** Username and password against the backend, which issues the session token. */
function CredentialSession({ info, children }: { info: AuthInfo; children: React.ReactNode }) {
  const provider = info.provider;
  const [token, setToken] = useState<string | undefined>(() => localStorage.getItem(TOKEN_KEY) ?? undefined);
  const [expired, setExpired] = useState(false);

  const signIn = useCallback(async (username: string, password: string) => {
    const token = await postLogin(username, password);
    localStorage.setItem(TOKEN_KEY, token);
    setExpired(false);
    setToken(token);
  }, []);

  const signOut = useCallback(() => {
    localStorage.removeItem(TOKEN_KEY);
    setToken((prev) => {
      if (prev) setExpired(true); // distinguish "signed out" from "never signed in"
      return undefined;
    });
  }, []);

  // The API layer reports an expired session by dispatching this event, so a
  // 401 anywhere in the app lands the user back on the sign-in screen.
  useEffect(() => {
    const onUnauthorized = () => signOut();
    window.addEventListener("devops-tools:unauthorized", onUnauthorized);
    return () => window.removeEventListener("devops-tools:unauthorized", onUnauthorized);
  }, [signOut]);

  const value = useMemo<Session>(
    () => ({
      provider,
      brand: brandOf(info),
      providerName: "",
      passwordLogin: info.passwordLogin ?? true,
      token,
      ready: true,
      expired,
      signIn,
      signOut,
    }),
    [provider, info.passwordLogin, token, expired, signIn, signOut]
  );
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

/** Delegates to an external identity provider through react-oidc-context. */
function OidcSession({ info, children }: { info: AuthInfo; children: React.ReactNode }) {
  const config = useMemo(
    () => ({
      authority: info.oidc!.issuerUrl,
      client_id: info.oidc!.clientId,
      redirect_uri: window.location.origin + "/",
      post_logout_redirect_uri: window.location.origin + "/",
      response_type: "code",
      scope: "openid profile email",
      automaticSilentRenew: true,
      userStore: new WebStorageStateStore({ store: window.localStorage }),
      // Strip ?code=...&state=... from the address bar after the redirect.
      onSigninCallback: () => {
        window.history.replaceState({}, document.title, window.location.pathname);
      },
    }),
    [info]
  );
  return (
    <AuthProvider {...config}>
      <OidcBridge
        passwordLogin={info.passwordLogin ?? false}
        brand={brandOf(info)}
        providerName={info.oidc?.displayName || "single sign-on"}
      >
        {children}
      </OidcBridge>
    </AuthProvider>
  );
}

/** Maps react-oidc-context state onto the shared Session shape. */
function OidcBridge({
  passwordLogin,
  brand,
  providerName,
  children,
}: {
  passwordLogin: boolean;
  brand: { name: string; initials: string };
  providerName: string;
  children: React.ReactNode;
}) {
  const oidc = useAuth();
  const [wasSignedIn, setWasSignedIn] = useState(false);
  // Set when the backend rejects a token the browser still had. Kept alongside
  // the provider's own state because the two can disagree: a token can be
  // within its lifetime and still be refused — a revoked session, a restarted
  // realm, a clock that drifted.
  const [rejected, setRejected] = useState(false);
  // A token this deployment issued to the break-glass account, if that is how
  // the session was started. It takes precedence: someone who signed in that
  // way did so because the provider was not an option.
  const [ownToken, setOwnToken] = useState<string | undefined>(
    () => localStorage.getItem(TOKEN_KEY) ?? undefined
  );

  useEffect(() => {
    if (oidc.isAuthenticated) setWasSignedIn(true);
  }, [oidc.isAuthenticated]);

  // A 401 from anywhere in the app ends the session here too.
  //
  // Without this the page stayed as it was, showing an error, with nothing to
  // press but "Sign out" — the one screen from which a portal offers no way
  // back into itself. The stored user is discarded so the sign-in screen is
  // what renders next.
  useEffect(() => {
    const onUnauthorized = () => {
      setRejected(true);
      if (localStorage.getItem(TOKEN_KEY)) {
        localStorage.removeItem(TOKEN_KEY);
        setOwnToken(undefined);
      }
      void oidc.removeUser();
    };
    window.addEventListener("devops-tools:unauthorized", onUnauthorized);
    return () => window.removeEventListener("devops-tools:unauthorized", onUnauthorized);
  }, [oidc]);

  const signIn = useCallback(async (username: string, password: string) => {
    const token = await postLogin(username, password);
    localStorage.setItem(TOKEN_KEY, token);
    setOwnToken(token);
  }, []);

  const signOut = useCallback(() => {
    if (ownToken) {
      localStorage.removeItem(TOKEN_KEY);
      setOwnToken(undefined);
      // Back to the break-glass form rather than on to the provider. There is
      // no provider session to end, and someone who signed in this way did so
      // because the provider was not available — sending them there on the way
      // out would strand them exactly when it matters.
      window.location.assign("/signin/local");
      return;
    }
    void oidc.signoutRedirect();
  }, [oidc, ownToken]);

  const value = useMemo<Session>(
    () => ({
      provider: "oidc",
      brand,
      providerName,
      passwordLogin,
      // Only while the provider still considers the session good.
      //
      // oidc.user outlives its own token: when a silent renewal fails the user
      // object stays put with an expired access token, and handing that out
      // meant every request 401ing against a page that looked signed in. That
      // is exactly what left people staring at "your session has expired" with
      // no way back to the sign-in screen.
      token: ownToken ?? (oidc.isAuthenticated ? oidc.user?.access_token : undefined),
      ready: !oidc.isLoading,
      // "Expired" rather than "never signed in" whenever there is something to
      // have lost: a user object left over from last time counts, even on a
      // fresh page load where this component has not seen anyone sign in.
      expired: !ownToken && (rejected || !!oidc.user || wasSignedIn) && !oidc.isAuthenticated,
      signIn,
      signOut,
    }),
    [oidc, wasSignedIn, rejected, ownToken, signIn, signOut, passwordLogin, brand, providerName]
  );
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

/** Starts the OIDC redirect; only meaningful in OIDC mode. */
export function useOidcSignIn(): (() => void) | undefined {
  const session = useContext(SessionContext);
  const oidc = useAuthSafely();
  if (!session || session.provider !== "oidc" || !oidc) return undefined;
  return () => void oidc.signinRedirect();
}

// useAuth throws when there is no AuthProvider above it, which is exactly the
// case for the local and ldap providers.
function useAuthSafely() {
  try {
    return useAuth();
  } catch {
    return undefined;
  }
}
