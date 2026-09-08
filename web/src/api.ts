// Thin wrapper over fetch: attaches the bearer token and parses the response.
// The /api prefix is proxied to the backend in development (see vite.config.ts).

export interface PodSummary {
  name: string;
  status: string;
  ready: string;
  restarts: number;
  node: string;
  age: string;
  /** The pod cannot pull its image — the one failure the list itself can offer
      to fix. From the container's current state, so it clears on recovery. */
  imageUnavailable: boolean;
  /** The tag that cannot be pulled; empty otherwise. */
  unavailableImage?: string;
}

export interface HelmRelease {
  name: string;
  namespace: string;
  revision: number;
  status: string;
  chart: string;
  version: string;
  updated: string;
}

/** Optional features, toggled by the deployment's configuration. */
export interface Capabilities {
  metrics: boolean;
  loki: boolean;
  // These two depend on the caller rather than the deployment: seeing who has
  // access and changing it are separate powers.
  users: boolean;
  manageUsers: boolean;
  /** A configuration source is enabled AND the caller was granted a path in it. */
  configurations: boolean;
}

/** One configuration entry, as listed. */
export interface ConfigEntry {
  path: string;
  writable: boolean;
}

/** One configuration entry with its content. */
export interface ConfigFile {
  path: string;
  content: string;
  /** Opaque version this was read at — Consul's index or a commit id — sent
      back on save so a concurrent change is refused rather than overwritten. */
  version: string;
  writable: boolean;
  /** Only Git knows these; Consul records no author. */
  author?: string;
  updatedAt?: string;
  /** Which side this came from, when both are configured. */
  side?: string;
  /** How the two sides stand on this file. Absent with a single source.
      Which one is newer is deliberately not claimed — Consul records no
      time at all, so any ordering would be invented. */
  drift?: "match" | "differ" | "missing-in-consul" | "missing-in-git";
}

/** One namespace a role reaches, and what it may do there. */
export interface NamespaceGrant {
  namespace: string;
  operations: string[];
}

/** One configuration path a role reaches, and what it may do there. */
export interface ConfigGrant {
  path: string;
  operations: string[];
}

export interface Role {
  name: string;
  description: string;
  namespaces: NamespaceGrant[];
  global: string[];
  configs: ConfigGrant[];
}

// PortalUser is someone who has signed in at least once. Groups come from the
// directory and are shown as context; roles are what the portal granted.
export interface PortalUser {
  username: string;
  email?: string;
  provider: string;
  groups: string[];
  roles: string[];
  firstSeen: string;
  lastSeen: string;
  // Held through auth.bootstrapAdmins, so it cannot be revoked from here.
  bootstrap: boolean;
  /** Created in the portal: its password can be changed here, and it can be
      deleted. Everything else is a record of someone who signed in. */
  managed: boolean;
  /** Roles prepared for a name nobody has signed in under yet. */
  invited?: boolean;
}

// What the namespace page gets. For a wildcard role `namespaces` is the
// cluster's current namespaces rather than the limit of the permission, so the
// page still lets a name be typed in — see handleNamespaces on the server.
export interface NamespaceList {
  namespaces: string[];
  wildcard: boolean;
}

export interface Me {
  username: string;
  email: string;
  roles: string[];
  groups?: string[];
  environment: string;
  authProvider: string;
  /** Build of the running instance; "dev" when built without a version. */
  version?: string;
  capabilities: Capabilities;
}

export interface PodEvent {
  type: string; // Normal | Warning
  reason: string;
  message: string;
  count: number;
  last: string; // RFC3339
}

export interface SecretSummary {
  name: string;
  type: string;
  keys: string[];
}

export interface SecretData {
  name: string;
  type: string;
  data: Record<string, string>;
}

export interface MetricPoint {
  t: number; // unix seconds
  v: number;
}

export interface PodMetrics {
  cpu: { usage: MetricPoint[]; limit: number };
  memory: { usage: MetricPoint[]; limit: number };
  jvm?: { heap: MetricPoint[]; nonheap: MetricPoint[]; gc: MetricPoint[] };
}

export interface RolloutStatus {
  kind: string;
  name: string;
  desired: number;
  updated: number;
  ready: number;
  available: number;
  done: boolean;
  deadlineExceeded: boolean;
  reason: string;
  reasonPod: string;
}

/** Carries the HTTP status so callers can tell cases apart (404, 409, …). */
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function handle(res: Response): Promise<Response> {
  if (res.status === 401) {
    // Tell the session layer to drop the token and show the sign-in screen.
    window.dispatchEvent(new Event("devops-tools:unauthorized"));
    throw new ApiError(401, "Your session has expired");
  }
  // Not "to this namespace": the same status now also answers a configuration
  // path, and naming the wrong thing sends people looking in the wrong place.
  if (res.status === 403) throw new ApiError(403, "You do not have access here");
  if (!res.ok) throw new ApiError(res.status, `Error ${res.status}: ${await res.text()}`);
  return res;
}

export function createApi(token: string | undefined) {
  const headers: Record<string, string> = token ? { Authorization: `Bearer ${token}` } : {};

  const getJSON = async <T>(path: string): Promise<T> =>
    handle(await fetch(`/api${path}`, { headers })).then((r) => r.json());

  const getText = async (path: string): Promise<string> =>
    handle(await fetch(`/api${path}`, { headers })).then((r) => r.text());

  const del = async (path: string): Promise<void> => {
    await handle(await fetch(`/api${path}`, { method: "DELETE", headers }));
  };

  const delJSON = async <T>(path: string): Promise<T> =>
    handle(await fetch(`/api${path}`, { method: "DELETE", headers })).then((r) => r.json());

  const post = async (path: string): Promise<void> => {
    await handle(await fetch(`/api${path}`, { method: "POST", headers }));
  };

  const postJSON = async <T>(path: string): Promise<T> =>
    handle(await fetch(`/api${path}`, { method: "POST", headers })).then((r) => r.json());

  const sendJSON = async (method: string, path: string, body: unknown): Promise<void> => {
    await handle(
      await fetch(`/api${path}`, {
        method,
        headers: { ...headers, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
    );
  };

  // Same, but hands back the response. Used where a success carries something
  // worth saying — a save that landed but left a copy behind, for instance.
  const sendJSONFor = async <T>(method: string, path: string, body: unknown): Promise<T> =>
    handle(
      await fetch(`/api${path}`, {
        method,
        headers: { ...headers, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
    ).then((r) => r.json());

  return {
    me: () => getJSON<Me>("/me"),
    namespaces: () => getJSON<NamespaceList>("/namespaces"),

    users: () => getJSON<PortalUser[]>("/users"),
    roles: () =>
      getJSON<{
        roles: Role[];
        operations: string[];
        /** How many people hold each role, by role name. */
        holders?: Record<string, number>;
      }>("/roles"),
    /** Grant or revoke one role across several people, in one write. */
    setRoleMembers: (role: string, users: string[], add: boolean) =>
      sendJSONFor<{
        changed: string[];
        unchanged: string[];
        unknown: string[];
        /** Revoked in the store, but still granted by auth.bootstrapAdmins. */
        stillGranted: string[];
      }>("POST", `/roles/${encodeURIComponent(role)}/members`, { users, add }),
    /** For the role editor only. Kept off /roles because listing means walking
        the whole repository, and both tabs of Access management load /roles. */
    configPaths: () =>
      getJSON<{ root: string; paths?: string[]; error?: string }>("/configurations/paths"),
    createUser: (body: { username: string; email: string; password: string; roles: string[] }) =>
      sendJSON("POST", "/users", body),
    /** Prepare roles for somebody who has not signed in yet. */
    invite: (body: { username: string; email: string; roles: string[] }) =>
      sendJSON("POST", "/users/invite", body),
    setUserRoles: (username: string, roles: string[]) =>
      sendJSON("PUT", `/users/${encodeURIComponent(username)}/roles`, { roles }),
    setUserPassword: (username: string, password: string) =>
      sendJSON("PUT", `/users/${encodeURIComponent(username)}/password`, { password }),
    deleteUser: (username: string) => del(`/users/${encodeURIComponent(username)}`),
    saveRole: (name: string, body: Omit<Role, "name">) =>
      sendJSON("PUT", `/roles/${encodeURIComponent(name)}`, body),
    deleteRole: (name: string) =>
      delJSON<{ affectedUsers: string[] }>(`/roles/${encodeURIComponent(name)}`),

    configs: (source?: string) =>
      getJSON<{
        entries: ConfigEntry[];
        granted: boolean;
        root: string;
        /** "consul", "git" or "git+consul" — where a save actually goes. */
        kind: string;
        /** Both sides, primary first; empty when only one is configured. */
        sides?: string[];
        /** Which side these entries came from. */
        side?: string;
      }>("/configurations" + (source ? `?source=${encodeURIComponent(source)}` : "")),
    config: (path: string, source?: string) =>
      getJSON<ConfigFile>(
        `/configurations/entry?path=${encodeURIComponent(path)}` +
          (source ? `&source=${encodeURIComponent(source)}` : "")
      ),
    /** The ordinary save: commits, then brings Consul up to date. */
    saveConfig: (path: string, content: string, version: string) =>
      sendJSONFor<{ status: string; warning?: string }>(
        "PUT",
        `/configurations/entry?path=${encodeURIComponent(path)}`,
        { content, version }
      ),
    /** The emergency save: one side only, deliberately leaving a difference. */
    saveConfigSide: (path: string, content: string, side: string) =>
      sendJSON(
        "PUT",
        `/configurations/entry?path=${encodeURIComponent(path)}&source=${encodeURIComponent(side)}`,
        { content }
      ),

    pods: (ns: string) =>
      getJSON<{ pods: PodSummary[]; canRebuildImage: boolean }>(`/namespaces/${ns}/pods`),
    podContainers: (ns: string, pod: string) =>
      getJSON<{ containers: string[]; app: string }>(`/namespaces/${ns}/pods/${pod}/containers`),
    workloadPods: (ns: string, app: string) =>
      getJSON<string[]>(`/namespaces/${ns}/workload-pods?app=${encodeURIComponent(app)}`),
    podEvents: (ns: string, pod: string) =>
      getJSON<{
        events: PodEvent[];
        imageUnavailable: boolean;
        crashLooping: boolean;
        /** Whether this person may act on it, here. False where the deployment
            does not manage builds at all. */
        canRebuildImage?: boolean;
        canDeploy?: boolean;
      }>(`/namespaces/${ns}/pods/${pod}/events`),

    /** Run the build that produced this pod's image again. */
    rebuildImage: (ns: string, pod: string) =>
      postJSON<{
        ticket?: string;
        job?: string;
        jobId?: number;
        jobStatus?: string;
        jobUrl?: string;
        alreadyRunning?: boolean;
        message?: string;
        /** The pipeline is gone: only a fresh one would help, and that deploys. */
        pipelineGone?: boolean;
        branch?: string;
        canStartPipeline?: boolean;
      }>(`/namespaces/${ns}/pods/${pod}/rebuild-image`),

    /** Start a fresh pipeline on the branch. This deploys — see pipeline-deploy. */
    startRebuildPipeline: (ns: string, pod: string) =>
      postJSON<{ ticket: string; pipelineId: number; pipelineUrl?: string; status?: string; branch: string }>(
        `/namespaces/${ns}/pods/${pod}/rebuild-image/pipeline`
      ),
    podDescribe: (ns: string, pod: string) => getText(`/namespaces/${ns}/pods/${pod}/describe`),
    podMetrics: (ns: string, pod: string, range: string) =>
      getJSON<PodMetrics>(`/namespaces/${ns}/pods/${pod}/metrics?range=${range}`),
    restartPod: (ns: string, pod: string) =>
      postJSON<{ status: string; kind: string; name: string }>(`/namespaces/${ns}/pods/${pod}/restart`),
    rolloutStatus: (ns: string, kind: string, name: string) =>
      getJSON<RolloutStatus>(
        `/namespaces/${ns}/rollout-status?kind=${encodeURIComponent(kind)}&name=${encodeURIComponent(name)}`
      ),

    podLogs: (ns: string, pod: string, tail = 500, container = "") =>
      getText(
        `/namespaces/${ns}/pods/${pod}/logs?tail=${tail}` +
          (container ? `&container=${encodeURIComponent(container)}` : "")
      ),
    multiPodLogs: (ns: string, pods: string[], tail = 500) =>
      getText(`/namespaces/${ns}/logs?pods=${encodeURIComponent(pods.join(","))}&tail=${tail}`),
    lokiLogs: (
      ns: string,
      opts: {
        pod?: string;
        pods?: string[];
        container?: string;
        scope?: "service";
        app?: string;
        filter?: string;
        from: number;
        to: number;
        limit?: number;
      }
    ) => {
      const q = new URLSearchParams();
      if (opts.app) q.set("app", opts.app);
      else if (opts.pods?.length) q.set("pods", opts.pods.join(","));
      else if (opts.pod) q.set("pod", opts.pod);
      if (opts.scope) q.set("scope", opts.scope);
      if (opts.container) q.set("container", opts.container);
      if (opts.filter) q.set("filter", opts.filter);
      q.set("from", String(opts.from));
      q.set("to", String(opts.to));
      if (opts.limit) q.set("limit", String(opts.limit));
      return getText(`/namespaces/${ns}/logs/query?${q.toString()}`);
    },

    releases: (ns: string) => getJSON<HelmRelease[]>(`/namespaces/${ns}/helm/releases`),
    releaseHistory: (ns: string, name: string) =>
      getJSON<HelmRelease[]>(`/namespaces/${ns}/helm/releases/${name}/history`),
    rollback: (ns: string, name: string, revision = 0) =>
      post(`/namespaces/${ns}/helm/releases/${name}/rollback?revision=${revision}`),
    uninstall: (ns: string, name: string) =>
      delJSON<{ status: string; release: string; warning?: string }>(
        `/namespaces/${ns}/helm/releases/${name}`
      ),

    secrets: (ns: string, includeSystem = false) =>
      getJSON<SecretSummary[]>(`/namespaces/${ns}/secrets${includeSystem ? "?system=true" : ""}`),
    secret: (ns: string, name: string) => getJSON<SecretData>(`/namespaces/${ns}/secrets/${name}`),
    createSecret: (ns: string, body: { name: string; type?: string; data: Record<string, string> }) =>
      sendJSON("POST", `/namespaces/${ns}/secrets`, body),
    updateSecret: (ns: string, name: string, data: Record<string, string>) =>
      sendJSON("PUT", `/namespaces/${ns}/secrets/${name}`, { data }),
    deleteSecret: (ns: string, name: string) => del(`/namespaces/${ns}/secrets/${name}`),
  };
}

export type Api = ReturnType<typeof createApi>;
