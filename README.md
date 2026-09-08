# DevOps Tools

**A self-service portal for Kubernetes.** Developers get logs, metrics,
restarts, secrets and Helm operations for the namespaces they are allowed to
touch — without handing out cluster access.

DevOps Tools sits between your developers and the cluster: it authenticates them,
decides what each role may do in which namespace, performs the operation with
its own service account, and writes every action to an audit trail.

---

## Why

Most teams end up in one of two places: either everyone gets `kubectl` access
they should not have, or every routine request — "what do the logs say?",
"can you restart it?" — becomes a ticket for the platform team.

DevOps Tools is the middle ground. Developers self-serve the routine work through
a browser; the platform team keeps the cluster credentials and gets an audit
log of everything that happened.

## What it does

| | |
|---|---|
| **Pods** | list, filter, inspect, describe, events |
| **Logs** | live tail with follow, level and text filters, container picker; historical logs from Loki including pods that no longer exist |
| **Metrics** | CPU, memory and JVM graphs from Prometheus |
| **Restarts** | rolling restart of the pod's workload, with live progress and a plain-language explanation when a rollout gets stuck |
| **Secrets** | browse, view, edit, create — every read is audited individually |
| **Helm** | releases, revision history, rollback, uninstall |

Metrics and historical logs are optional: leave them
unconfigured and the corresponding UI simply does not appear.

## Authentication

Pick one provider in the configuration:

- **`local`** — accounts defined in the config file. Nothing else to install;
  the fastest way to try DevOps Tools or to run it in an air-gapped environment.
- **`ldap`** — bind against Active Directory or OpenLDAP over LDAPS.
- **`oidc`** — delegate to Keycloak, Dex, Okta, Authentik or anything else
  speaking OpenID Connect. If you already run an identity provider this is the
  recommended option — it usually federates your directory anyway.

  Roles defined in the provider are not read. With `oidc`, set
  `auth.bootstrapAdmins.users` to the `preferred_username` of whoever should
  administer the portal, or nobody will be able to grant the first role — the
  service refuses to start otherwise. People are identified by
  `preferred_username`, so renaming someone in the provider makes them a new
  user here, without the roles the old record held.

With `local` and `ldap` the application issues its own signed session tokens;
with `oidc` it only verifies the ones your provider issues.

Whichever you choose, the provider only establishes **who** someone is. What
they may do is decided in DevOps Tools.

## Authorization

A role grants operations in namespaces. This part is deployed with the chart:

```yaml
roles:
  developer:
    namespaces:
      - namespace: payments-dev
        operations: [logs, describe, metrics, pod-restart, secret-list]
```

Roles are created and edited under **Roles** in the UI, and who holds one is set
under **Users**. Both live on a persistent volume, so a `helm upgrade` cannot
undo them — the `rbacConfig` in the chart seeds an empty installation and is
never applied again.

With the `local` provider, accounts are created in the UI too: there is no
directory for people to arrive from, so an administrator hands out usernames and
passwords. With `ldap` or `oidc` people appear the first time they sign in, with
no access at all, and are granted a role from there.

Roles are read on every request rather than carried in the session token, so
revoking access takes effect at once instead of whenever the session happens to
expire.

Since access is stored rather than deployed, a mistake there could lock everyone
out. `auth.bootstrapAdmins` is the way back in: those users always hold the
roles listed, and nothing in the UI can change it.

That account can also be given a password of its own, through
`auth.bootstrapAdmins.passwordLogin`, which works whatever the provider is —
for the first sign-in of a new installation, and for when the identity provider
is unreachable. Configuring it puts a username and password form on the sign-in
screen next to the identity provider button, whichever provider is in use.

It is a route in that your directory cannot revoke, so it is off unless
configured, logged at every start and at every use, and rate limited like any
other password form.

Its password, and every other credential this needs, can be written in the
chart's values or taken from a Secret you already have. Both are named beside
the setting they belong to, so what an installation needs is visible in one
place rather than spread between a values file and a Secret whose keys have to
line up:

| what | written in values | or from your Secret | arrives as |
| --- | --- | --- | --- |
| administrator password | `auth.bootstrapAdmins.passwordLogin.password` | `…passwordSecret.name` | `DEVOPS_TOOLS_BOOTSTRAP_PASSWORD` |
| directory bind password | `auth.ldap.bindPassword` | `auth.ldap.bindPasswordSecret.name` | `DEVOPS_TOOLS_LDAP_BIND_PASSWORD` |
| directory CA certificate | `auth.ldap.tls.caCert` | `auth.ldap.tls.caCertSecret.name` | a mounted file |
| Consul token | `configurations.consul.token` | `configurations.consul.tokenSecret.name` | `CONSUL_HTTP_TOKEN` |
| GitLab token | `configurations.git.token` | `configurations.git.tokenSecret.name` | `GITLAB_TOKEN` |

Each `…Secret` also takes a `key`, which defaults to the variable in the last
column — `ca.crt` for the certificate.

These are the only settings under `config` that the chart does not render into
the ConfigMap: read access to one of those is handed out far more freely than
to a Secret. A value written in values is moved into the chart's own Secret; a
Secret you name is referenced where it already stands, so there is nothing to
keep in step when you rotate it.

Writing a value in values does leave it in that file, and files get committed.
Setting both forms for the same credential is refused rather than resolved —
which one won would be an ordering, not a decision.

A plain password is hashed at startup whichever way it arrives, and never
written anywhere. Where even the Secret should not hold something readable,
`DEVOPS_TOOLS_BOOTSTRAP_PASSWORD_HASH` takes a bcrypt hash instead.

The service account DevOps Tools itself uses is the outer boundary; this model
narrows it down per user. Both are enforced — a user can never do more than the
service account could.

## Naming

What the interface calls itself is a setting — `config.ui.brandName` — and
changing it costs nothing:

```yaml
config:
  ui:
    brandName: "Platform Console"
```

Renaming the software itself is a different matter: the volume holding the
access model is named after the release, so a rename can leave it behind and
present an empty portal. [docs/renaming.md](docs/renaming.md) has the procedure
and the order that avoids it.

## State and backups

DevOps Tools keeps one file: who has signed in and what they were granted. It lives
on the volume the chart claims, which is retained when the release is removed.
Everything else is deployed with the chart and needs no backup.

`kubectl cp` cannot fetch it: that command runs `tar` inside the container, and
this image is distroless — no shell, no tar, nothing to run. Take the file from
the volume instead, with the workload stopped so nothing is writing to it:

```bash
kubectl -n devops-tools scale deploy/devops-tools --replicas=0
kubectl -n devops-tools run access-backup --rm --attach --restart=Never   --image=busybox --quiet   --overrides='{"spec":{"containers":[{"name":"c","image":"busybox","command":["cat","/data/devops-tools.json"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}],"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"devops-tools"}}]}}'   > devops-tools-access-backup.json
kubectl -n devops-tools scale deploy/devops-tools --replicas=1
```

The claim is named after the release. Restoring is the same shape with the file
going the other way.

Set `persistence.storageClass` on a cluster without a default class, or the
claim stays `Pending` and the pod never starts. Only one replica is supported
while persistence is on — the chart refuses to render otherwise.

## Install

The image and the chart are both published:

```bash
helm install devops-tools oci://registry-1.docker.io/1kcsd/devops-tools \
  --namespace devops-tools --create-namespace \
  --set config.auth.bootstrapAdmins.passwordLogin.password='pick-something'
```

Many organisations will prefer to build a tool with these permissions
themselves. Pass `VERSION` so the running instance can say which build it is —
it appears in the sidebar and in the startup log; without it the build calls
itself `dev`.

```bash
git clone https://github.com/devops-tools/devops-tools
cd devops-tools

VERSION=0.25.0
docker build --build-arg VERSION=$VERSION -t registry.example.com/devops-tools:$VERSION .
docker push registry.example.com/devops-tools:$VERSION

helm install devops-tools ./charts/devops-tools \
  --namespace devops-tools --create-namespace \
  --set image.repository=registry.example.com/devops-tools \
  --set config.auth.bootstrapAdmins.passwordLogin.password='pick-something'
```

That password is the one thing an install has to decide. Nothing here ships a
default one, because a password published in a repository is a known password
rather than a weak one — so with none configured the service stops at startup
and says which of the two settings to fill in, rather than coming up as a
portal anybody could walk into.

Then port-forward and sign in as `admin` with it:

```bash
kubectl -n devops-tools port-forward svc/devops-tools 8080:80
```

That account holds `platform-admin` and cannot be locked out by anything done in
the UI. Everyone else — accounts under the `local` provider, or people arriving
from your directory — is created and granted from there.

See [`charts/devops-tools/values.yaml`](charts/devops-tools/values.yaml) for the full
set of options and [`config/example/`](config/example/) for a documented
configuration.

### Permissions

By default the chart creates a `ClusterRole`, so one installation can serve
every namespace. If that is not acceptable, `rbac.scope: namespace` restricts
it to the release namespace instead.

Uninstalling a Helm release means deleting whatever that release created. If
your charts contain custom resources, add their API groups under
`rbac.extraRules` — otherwise the uninstall leaves them behind and reports a
partial failure.

## Development

```bash
# backend — needs a kubeconfig, serves on :8080
DEVOPS_TOOLS_CONFIG_DIR=./config/example go run ./cmd/server

# frontend — dev server on :5173, proxies /api to the backend
npm --prefix web install
npm --prefix web run dev
```

The production image embeds the compiled frontend into the Go binary, so a
deployment is a single container with no web server in front of it.

## Configuration reference

Configuration is read from a directory of YAML files (a mounted ConfigMap in
Kubernetes) or, optionally, from Consul KV. The access model is reloaded
without a restart when the source supports it.

Secrets never live in the configuration file:

| Variable | Purpose |
|---|---|
| `DEVOPS_TOOLS_SESSION_KEY` | signs session tokens; required when running more than one replica |
| `DEVOPS_TOOLS_LDAP_BIND_PASSWORD` | password of the LDAP service account |

## Security notes

- Every action is written to a structured audit log and counted in a Prometheus
  metric — including reads of individual secrets and failed sign-in attempts.
- Password logins are rate limited per account.
- The container runs as a non-root user from a distroless base image with a
  read-only root filesystem.
- `/metrics` is unauthenticated by design; restrict it at the ingress if that
  is not acceptable in your environment.

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Copyright 2026 Kazakhstan Central Security Depository.
