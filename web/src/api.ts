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
  /** Where a release installed from the catalog came from. */
  catalog?: { repo: string; chart: string };
}

/** One chart in the catalog. */
export interface CatalogPackage {
  repo: string;
  repoTitle: string;
  name: string;
  description: string;
  version: string;
  appVersion?: string;
  logo?: string;
  stars: number;
  official?: boolean;
  deprecated?: boolean;
}

/** One version of a chart, ready to fill in. */
export interface CatalogChart extends CatalogPackage {
  repoUrl: string;
  /** The chart's page on Artifact Hub. */
  page?: string;
  readme?: string;
  versions: { version: string; appVersion?: string; prerelease?: boolean }[];
  digest: string;
  /** The chart's own values.yaml. */
  defaults: string;
  schema: boolean;
  suggestedName: string;
  /** What the chart's values let the portal fill in from the cluster. */
  settings: CatalogSettings;
  /** What the cluster offers; only when a namespace was given. */
  cluster?: ClusterHints;
}

export interface IngressSpot {
  path: string;
  shape: "hostname" | "hosts" | "hostlist";
  classKey: string;
}

export interface CatalogSettings {
  storage: string[];
  ingresses: IngressSpot[];
}

export interface ClusterHints {
  storageClasses: { name: string; default?: boolean }[];
  ingressClasses: string[];
  ingressClass: string;
  domain: string;
  certs: { name: string; names: string[]; days: number }[];
}

export interface ValuesFill {
  storageClass?: string;
  storagePaths?: string[];
  ingress?: IngressSpot & { host: string; class: string; tls: string };
}

export interface CatalogProblem {
  level: "error" | "warn";
  text: string;
}

/** What installing a chart with some values would do. */
export interface CatalogCheck {
  ok: boolean;
  digest: string;
  objects: { kind: string; name: string; hook?: boolean }[];
  problems: CatalogProblem[];
  images: string[];
  imageStates: { image: string; missing?: boolean; unknown?: string }[];
  fromVersion?: string;
}

export interface CatalogAsk {
  kind: "install" | "upgrade";
  release: string;
  repo: string;
  chart: string;
  version: string;
  values: string;
}

export interface HelmRequest {
  id: string;
  kind: "install" | "upgrade";
  namespace: string;
  release: string;
  repo: string;
  chart: string;
  version: string;
  fromVersion?: string;
  digest: string;
  values: string;
  /** What was asked for, when an administrator changed the values. */
  askedValues?: string;
  objects?: string[];
  requester: string;
  created: string;
  expires: string;
  status: "waiting" | "installing" | "installed" | "failed" | "rejected" | "withdrawn" | "expired";
  decider?: string;
  decided?: string;
  comment?: string;
  detail?: string;
}

/** A failed catalog call: the server's own words, and the check when one failed. */
export class CatalogError extends Error {
  status: number;
  check?: CatalogCheck;
  constructor(status: number, message: string, check?: CatalogCheck) {
    super(message);
    this.status = status;
    this.check = check;
  }
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
  /** A trail can be read at all, AND the caller holds audit-read somewhere. */
  audit: boolean;
  /** The caller holds service-list in at least one namespace. */
  services: boolean;
  /** The caller holds ingress-list in at least one namespace. */
  ingresses?: boolean;
  /** May take a snapshot of the access model, and put one back. */
  accessRestore: boolean;
  /** May ask for an install or an upgrade somewhere: the Helm Packages page. */
  helmPackages?: boolean;
  /** Answers requests to install from the catalog. */
  helmRequests?: boolean;
}

/** Where the access model lives, and how much of it there is. */
export interface StorageStat {
  /** "secret <namespace>/<name>" or "file <path>". */
  backend: string;
  /** What the model occupies where it is kept — the number the limit applies
      to, and smaller than `raw` once the backend starts compressing. */
  bytes: number;
  /** The document before the backend packed it. Equal to `bytes` while nothing
      is being compressed. */
  raw: number;
  /** The backend's ceiling, or 0 where it has none worth showing. */
  limit: number;
  users: number;
  withRole: number;
  roles: number;
  schema: number;
  /** This replica's copy is the stored one. False means a change landed
      elsewhere and has not arrived here yet. */
  current: boolean;
  /** When this replica last wrote. Absent if it has not since it started. */
  lastWrite?: string;
  /** Pods serving this release. Absent outside a cluster. */
  replicas?: number;
  /** Whether this caller may take a snapshot or put one back. */
  mayRestore: boolean;
}

/** One recorded action. */
export interface AuditEntry {
  time: string;
  user: string;
  /** Empty for the actions that belong to no namespace: sign-ins, grants. */
  namespace: string;
  operation: string;
  target: string;
  allowed: boolean;
  success: boolean;
  error: string;
  /** What the action changed — which keys, which settings, the commit — never
      what they were changed to. Empty on records written before it existed. */
  detail: string;
  /** The shell session an entry belongs to, and its place in it. */
  session?: string;
  seq?: number;
}

export interface AuditPage {
  entries: AuditEntry[];
  /** Unix milliseconds to pass as `to` for the next page. Zero at the horizon,
      which is what stops the interface offering to load more. */
  next: number;
  /** "loki" reaches back as far as its retention; "pods" only as far as the
      node has kept the logs, which is hours. */
  source: "loki" | "pods" | "";
  /** Seconds. How far back this deployment will look. */
  horizon: number;
  /** The operation vocabulary, for the filter. */
  operations: string[];
  /** What the caller may see, for the interface to say so plainly. */
  namespaces: string[] | null;
  allNamespaces: boolean;
  global: boolean;
  /** Where older events went, if anywhere. Both empty means nothing to say. */
  archiveNote: string;
  archiveURL: string;
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

/** One port a service answers on. */
export interface ServicePort {
  name?: string;
  port: number;
  /** Where the pod listens: a number, or the name of a port in the pod spec. */
  targetPort: string;
  protocol: string;
  appProtocol?: string;
  nodePort?: number;
  /** The ports the pods declare, present only when targetPort is not one of
      them. What the pods say, not proof that nothing listens. */
  declared?: string[];
}

/** A service as the Services page shows it: where it answers. */
export interface ServiceInfo {
  name: string;
  namespace: string;
  /** ClusterIP, NodePort, LoadBalancer or ExternalName. */
  type: string;
  headless: boolean;
  /** The outside host an ExternalName service stands for. */
  externalName?: string;
  ports: ServicePort[];
  hasSelector: boolean;
  /** Pods behind the service. Absent when they could not be read — which is
      not the same as none. */
  endpoints?: { ready: number; total: number };
  /** The pods behind it, ready or not, at most twenty. */
  pods?: string[];
  /** The Helm release that installed it, and its chart label. */
  release?: string;
  chart?: string;
}

/** The services in one namespace. */
export interface ServiceList {
  services: ServiceInfo[];
  /** The suffix of a fully qualified name, e.g. "cluster.local". */
  clusterDomain: string;
  /** Which of a row's actions lead somewhere this person may go. */
  mayLogs: boolean;
  mayHelm: boolean;
  mayRequest: boolean;
  /** May see the ingresses here: the Ingress tab is offered. */
  mayIngress?: boolean;
}

export interface Header {
  name: string;
  value: string;
}

/** A request kept in a service's collection. Secrets are referenced as
    {{secret:name/key}}, never stored. */
export interface SavedRequest {
  id?: string;
  folder?: string;
  name: string;
  method: string;
  /** Service port; 0 or absent means the first. */
  port?: number;
  path: string;
  headers?: Header[];
  body?: string;
}

export interface RequestCollection {
  collection: {
    requests: SavedRequest[];
    /** Sent back with a save, so concurrent saves do not overwrite. */
    version: string;
    /** Managed by a chart or GitOps: shown, not changed here. */
    readOnly: boolean;
    managedBy?: string;
  };
  mayWrite: boolean;
  /** {{secret:…}} will be filled in for this person. */
  maySecrets: boolean;
}

export interface RequestResponse {
  url: string;
  status?: number;
  statusText?: string;
  ms: number;
  headers?: Header[];
  body?: string;
  base64?: boolean;
  truncated?: boolean;
  size: number;
  /** Why there was no answer. */
  error?: string;
}

export interface APIEndpoint {
  method: string;
  path: string;
  summary?: string;
  tag?: string;
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
  /** A Helm release or a service account token: shown, never changed. */
  system?: boolean;
}

/** A ConfigMap as the list shows it. */
/** What opening a shell needs: the ticket for the WebSocket, and its limits. */
export interface ExecTicket {
  ticket: string;
  container: string;
  shell: string;
  idleSeconds: number;
  /** When the session ends at the latest, in epoch milliseconds. */
  until: number;
}

/** What to do about a problem: use a port or a certificate, or go and look. */
export interface IngressFix {
  kind: "port" | "tls" | "pods" | "ingress";
  value?: string;
  namespace?: string;
  name?: string;
}

export interface IngressProblem {
  level: "warn" | "error";
  text: string;
  fix?: IngressFix;
}

/** One host and path an ingress answers. */
export interface IngressRoute {
  ingress: string;
  /** Empty for the default backend: any host. */
  host: string;
  path: string;
  service: string;
  port: string;
  ready?: { ready: number; total: number };
  tlsSecret?: string;
  tlsDays?: number;
  class?: string;
  release?: string;
  problems: IngressProblem[];
}

export interface IngressCert {
  secret: string;
  names: string[];
  days: number;
}

export interface IngressList {
  routes: IngressRoute[];
  annotationKeys: string[];
  classes: string[];
  certificates: IngressCert[];
  mayCreate: boolean;
  mayUpdate: boolean;
  mayDelete: boolean;
}

export interface IngressRule {
  host: string;
  path: string;
  pathType?: string;
  service: string;
  port: string;
}

export interface IngressForm {
  class: string;
  rules: IngressRule[];
  tls: { secret: string; hosts: string[] }[];
  annotations: Record<string, string>;
}

export interface IngressDoc extends IngressForm {
  name: string;
  version: string;
  release?: string;
  yaml: string;
  defaultBackend?: string;
  routes: IngressRoute[];
}

export interface IngressSuggestion {
  name: string;
  host: string;
  path: string;
  port: string;
  ports: string[];
  class: string;
  tls?: string;
  secrets: string[];
}

export interface ConfigMapSummary {
  name: string;
  keys: number;
  /** The Helm release that installed it: its next deploy undoes edits. */
  release?: string;
  immutable?: boolean;
  /** CA bundles and the portal's own: hidden unless asked for. */
  system?: boolean;
  /** Workloads that read it; absent when they could not be read. */
  usedBy?: number;
}

export interface ConfigMapList {
  configMaps: ConfigMapSummary[];
  mayCreate: boolean;
  mayUpdate: boolean;
  mayDelete: boolean;
  mayRestart: boolean;
}

/** How one workload reads a ConfigMap. Env values are fixed at start; mounted
    files refresh within a minute, except under subPath. */
export interface ConfigMapUsage {
  kind: string;
  name: string;
  envAll?: boolean;
  env?: string[];
  mounts?: { path: string; keys?: string[]; subPath?: boolean }[];
}

export interface ConfigMapData {
  name: string;
  data: Record<string, string>;
  /** The data: block as kubectl shows it. */
  yaml: string;
  binaryKeys?: string[];
  release?: string;
  immutable?: boolean;
  /** Kept by the cluster or the portal: shown, never changed. */
  system?: boolean;
  /** Sent back with a change, so concurrent edits do not overwrite. */
  version: string;
  usedBy: ConfigMapUsage[];
  usageUnknown?: boolean;
}

export interface SecretData {
  name: string;
  type: string;
  data: Record<string, string>;
  system?: boolean;
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

  // The catalog answers its errors as {"error": …}, with the failed check
  // beside it when that is the reason; both are worth showing as they are.
  const catalogCall = async <T>(method: string, path: string, body?: unknown): Promise<T> => {
    const res = await fetch(`/api${path}`, {
      method,
      headers: body === undefined ? headers : { ...headers, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 401 || res.status === 403) await handle(res);
    if (!res.ok) {
      const text = await res.text();
      let msg = text;
      let check: CatalogCheck | undefined;
      try {
        const j = JSON.parse(text);
        msg = j.error ?? text;
        check = j.check;
      } catch {
        /* not JSON: the text as it is */
      }
      throw new CatalogError(res.status, msg, check);
    }
    return res.json();
  };

  return {
    me: () => getJSON<Me>("/me"),
    namespaces: () => getJSON<NamespaceList>("/namespaces"),

    /** One page of the audit trail, newest first. Paging walks backwards: pass
        the `next` of the previous page as `to`. */
    audit: (q: {
      to?: number;
      from?: number;
      operation?: string;
      user?: string;
      filter?: string;
      limit?: number;
    }) => {
      const p = new URLSearchParams();
      if (q.to) p.set("to", String(q.to));
      if (q.from) p.set("from", String(q.from));
      if (q.operation) p.set("operation", q.operation);
      if (q.user) p.set("user", q.user);
      if (q.filter) p.set("filter", q.filter);
      if (q.limit) p.set("limit", String(q.limit));
      const qs = p.toString();
      return getJSON<AuditPage>("/audit" + (qs ? `?${qs}` : ""));
    },

    /** The namespaces the caller holds service-list in. */
    serviceNamespaces: () => getJSON<{ namespaces: string[] }>("/services"),
    /** The services in one namespace. */
    services: (ns: string) => getJSON<ServiceList>(`/namespaces/${ns}/services`),
    requestCollection: (ns: string, svc: string) =>
      getJSON<RequestCollection>(`/namespaces/${ns}/services/${encodeURIComponent(svc)}/requests`),
    /** Adds, replaces and removes saved requests in one write. */
    saveRequests: (
      ns: string,
      svc: string,
      change: { version: string; save?: SavedRequest[]; remove?: string[] }
    ) =>
      sendJSONFor<RequestCollection["collection"]>(
        "PUT",
        `/namespaces/${ns}/services/${encodeURIComponent(svc)}/requests`,
        change
      ),
    sendRequest: (
      ns: string,
      svc: string,
      req: { method: string; port: number; path: string; headers: Header[]; body: string }
    ) =>
      sendJSONFor<RequestResponse>(
        "POST",
        `/namespaces/${ns}/services/${encodeURIComponent(svc)}/send`,
        req
      ),
    /** The operations the service describes in its OpenAPI document. */
    serviceApiDocs: (ns: string, svc: string, port: number) =>
      getJSON<{ source?: string; endpoints: APIEndpoint[] }>(
        `/namespaces/${ns}/services/${encodeURIComponent(svc)}/api-docs?port=${port}`
      ),

    storage: () => getJSON<StorageStat>("/access/storage"),
    /** The model as a file, for keeping or for moving to another environment. */
    snapshot: async (): Promise<Blob> =>
      handle(await fetch("/api/access/snapshot", { headers })).then((r) => r.blob()),
    /** Replace the whole model. The body is the snapshot itself, not a wrapper. */
    restore: async (snapshot: string): Promise<{ users: number; roles: number }> =>
      handle(
        await fetch("/api/access/snapshot", {
          method: "PUT",
          headers: { ...headers, "Content-Type": "application/json" },
          body: snapshot,
        })
      ).then((r) => r.json()),

    users: () => getJSON<PortalUser[]>("/users"),
    roles: () =>
      getJSON<{
        roles: Role[];
        /** What this deployment offers, with the switched-off features left out. */
        operations: string[];
        /** Which section of the editor each one belongs in. Sent by the server
            rather than known here: a copy of it here went stale twice, and both
            times a permission ended up with no checkbox at all. An operation
            can be in two of these — audit-read means different things per
            namespace and globally. */
        namespaceOperations: string[];
        globalOperations: string[];
        configurationOptions: string[];
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
      getJSON<{ containers: string[]; app: string; mayExec?: boolean }>(`/namespaces/${ns}/pods/${pod}/containers`),
    /** A ticket to open a shell in a container, and how long the session may last. */
    execTicket: (ns: string, pod: string, container: string) =>
      sendJSONFor<ExecTicket>("POST", `/namespaces/${ns}/pods/${pod}/exec`, { container }),
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

    /** The namespaces a chart may be installed into. */
    catalogNamespaces: () => catalogCall<{ namespaces: string[] }>("GET", "/helm/namespaces"),
    catalogAccess: () => catalogCall<{ install: boolean; upgrade: boolean; direct: boolean }>("GET", "/helm/access"),
    /** Releases installed from the catalog, in every namespace the caller may ask in. */
    catalogReleases: () =>
      catalogCall<(HelmRelease & { mayUpgrade: boolean; mayList: boolean })[]>("GET", "/helm/catalog-releases"),
    catalogSearch: (q: string, offset = 0) =>
      catalogCall<{ packages: CatalogPackage[]; total: number }>(
        "GET",
        `/helm/catalog?q=${encodeURIComponent(q)}&offset=${offset}`
      ),
    /** One version of a chart; with a namespace, a release name free there. */
    catalogChart: (repo: string, chart: string, version = "", ns = "") => {
      const q = new URLSearchParams();
      if (version) q.set("version", version);
      if (ns) q.set("namespace", ns);
      return catalogCall<CatalogChart>(
        "GET",
        `/helm/catalog/${encodeURIComponent(repo)}/${encodeURIComponent(chart)}?${q.toString()}`
      );
    },
    catalogRelease: (ns: string, name: string) =>
      catalogCall<{ origin: { repo: string; chart: string }; version: string; values: string }>(
        "GET",
        `/namespaces/${ns}/helm/releases/${encodeURIComponent(name)}/catalog`
      ),
    catalogCheck: (ns: string, ask: CatalogAsk) =>
      catalogCall<CatalogCheck>("POST", `/namespaces/${ns}/helm/check`, ask),
    catalogRequest: (ns: string, ask: CatalogAsk) =>
      catalogCall<HelmRequest>("POST", `/namespaces/${ns}/helm/requests`, ask),
    /** Writes what was filled from the cluster into the values. */
    /** Brings the values from what `previous` filled in to what `fill` does;
        `kept` are keys changed by hand since, which the form leaves alone. */
    fillValues: (values: string, fill: ValuesFill, previous: ValuesFill) =>
      catalogCall<{ values: string; kept: string[] }>("POST", "/helm/values/fill", { values, ...fill, previous }),
    /** Requests in every namespace the caller works in. */
    myRequests: () => catalogCall<HelmRequest[]>("GET", "/helm/my-requests"),
    withdrawRequest: (ns: string, id: string) =>
      catalogCall<HelmRequest>("DELETE", `/namespaces/${ns}/helm/requests/${id}`),
    allRequests: () => catalogCall<HelmRequest[]>("GET", "/helm/requests"),
    requestCount: () => catalogCall<{ waiting: number }>("GET", "/helm/requests/count"),
    checkRequest: (id: string, values: string) =>
      catalogCall<CatalogCheck>("POST", `/helm/requests/${id}/check`, { values }),
    approveRequest: (id: string, values: string) =>
      catalogCall<HelmRequest>("POST", `/helm/requests/${id}/approve`, { values }),
    rejectRequest: (id: string, comment: string) =>
      catalogCall<HelmRequest>("POST", `/helm/requests/${id}/reject`, { comment }),

    secrets: (ns: string, includeSystem = false) =>
      getJSON<SecretSummary[]>(`/namespaces/${ns}/secrets${includeSystem ? "?system=true" : ""}`),
    secret: (ns: string, name: string) => getJSON<SecretData>(`/namespaces/${ns}/secrets/${name}`),
    createSecret: (ns: string, body: { name: string; type?: string; data: Record<string, string> }) =>
      sendJSON("POST", `/namespaces/${ns}/secrets`, body),
    updateSecret: (ns: string, name: string, data: Record<string, string>) =>
      sendJSON("PUT", `/namespaces/${ns}/secrets/${name}`, { data }),
    deleteSecret: (ns: string, name: string) => del(`/namespaces/${ns}/secrets/${name}`),

    ingressNamespaces: () => getJSON<{ namespaces: string[] }>("/ingresses"),
    ingresses: (ns: string) => getJSON<IngressList>(`/namespaces/${ns}/ingresses`),
    ingress: (ns: string, name: string) =>
      getJSON<IngressDoc>(`/namespaces/${ns}/ingresses/${encodeURIComponent(name)}`),
    ingressSuggestion: (ns: string, service: string) =>
      getJSON<IngressSuggestion>(`/namespaces/${ns}/ingress-suggestion?service=${encodeURIComponent(service)}`),
    /** The list's checks, run on an ingress before it is saved. */
    checkIngress: (ns: string, name: string, form: IngressForm) =>
      sendJSONFor<{ routes: IngressRoute[] }>("POST", `/namespaces/${ns}/ingress-check`, { name, form }),
    createIngress: (ns: string, name: string, form: IngressForm) =>
      sendJSONFor<{ detail: string }>("POST", `/namespaces/${ns}/ingresses`, { name, form }),
    updateIngress: (ns: string, name: string, version: string, change: { form: IngressForm } | { yaml: string }) =>
      sendJSONFor<{ ingress: IngressDoc; detail: string }>(
        "PUT",
        `/namespaces/${ns}/ingresses/${encodeURIComponent(name)}`,
        { version, ...change }
      ),
    deleteIngress: (ns: string, name: string) => del(`/namespaces/${ns}/ingresses/${encodeURIComponent(name)}`),

    configMaps: (ns: string) => getJSON<ConfigMapList>(`/namespaces/${ns}/configmaps`),
    configMap: (ns: string, name: string) =>
      getJSON<ConfigMapData>(`/namespaces/${ns}/configmaps/${encodeURIComponent(name)}`),
    /** Creates from a name and values, or from a manifest (the import). */
    createConfigMap: (
      ns: string,
      body: { name: string; data?: Record<string, string>; yaml?: string } | { manifest: string }
    ) => sendJSONFor<{ name: string }>("POST", `/namespaces/${ns}/configmaps`, body),
    /** Saves either the values, or the data: block as YAML text. */
    updateConfigMap: (
      ns: string,
      name: string,
      version: string,
      change: { data: Record<string, string> } | { yaml: string }
    ) =>
      sendJSONFor<{ configMap: ConfigMapData; detail: string }>(
        "PUT",
        `/namespaces/${ns}/configmaps/${encodeURIComponent(name)}`,
        { version, ...change }
      ),
    deleteConfigMap: (ns: string, name: string) =>
      del(`/namespaces/${ns}/configmaps/${encodeURIComponent(name)}`),
    /** The ConfigMap as a manifest, for another environment. */
    exportConfigMap: (ns: string, name: string) =>
      getText(`/namespaces/${ns}/configmaps/${encodeURIComponent(name)}/export`),
    /** Rolls a workload out, as kubectl rollout restart does. */
    restartWorkload: (ns: string, kind: string, name: string) =>
      postJSON<{ status: string }>(
        `/namespaces/${ns}/workloads/${kind}/${encodeURIComponent(name)}/restart`
      ),
  };
}

export type Api = ReturnType<typeof createApi>;
