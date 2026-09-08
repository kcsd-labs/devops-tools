# Renaming

Two different things go by "the name", and they cost very different amounts to
change.

## Just what it is called

`config.ui.brandName` — the sidebar, the sign-in screen and the browser tab.
Nothing else is affected, nothing has to be migrated, and it can be changed as
often as you like.

```yaml
config:
  ui:
    brandName: "Platform Console"
    brandInitials: "" # the two-letter mark; derived from the name when empty
```

For most installations this is the whole answer. What follows is only for
renaming the software itself.

## Renaming the software

The name is bound into four things beyond the interface. Three are mechanical;
the first will lose your data if you get it wrong.

### 1. The volume — the one that matters

Everything set up in the UI — who has signed in, which roles exist, who holds
them — lives on a PersistentVolumeClaim named after the release. Rename the
chart and Helm computes a different name, creates an empty volume, and leaves
the old one behind. The portal comes up looking as though every user and role
had vanished. Nothing is actually lost, but that is not obvious at the time.

Pin the old name before renaming anything:

```yaml
# Keeps every resource under the name it already has, chart name notwithstanding.
fullnameOverride: devops-tools
```

or, to rename the rest and keep only the volume:

```yaml
persistence:
  existingClaim: devops-tools
```

Check what you actually have before and after — `kubectl -n <ns> get pvc` — and
confirm the pod mounted the one with the data in it.

The same applies to the Secret holding the session signing key: it is retained
on uninstall, and a renamed release will generate a new one, signing everybody
out once. Harmless, but expect it.

### 2. Environment variables

The prefix is `DEVOPS_TOOLS_`. Renaming it means recreating every Secret and
`extraEnv` entry that carries one:

```
DEVOPS_TOOLS_CONFIG_DIR              DEVOPS_TOOLS_SESSION_KEY
DEVOPS_TOOLS_CONFIG_SOURCE           DEVOPS_TOOLS_BOOTSTRAP_PASSWORD
DEVOPS_TOOLS_CONSUL_ADDR             DEVOPS_TOOLS_BOOTSTRAP_PASSWORD_HASH
DEVOPS_TOOLS_CONSUL_PREFIX           DEVOPS_TOOLS_LDAP_BIND_PASSWORD
DEVOPS_TOOLS_ENVIRONMENT             DEVOPS_TOOLS_LDAP_BIND_DN
DEVOPS_TOOLS_SERVER_ADDR             DEVOPS_TOOLS_LDAP_URL
DEVOPS_TOOLS_CORS_ORIGIN             DEVOPS_TOOLS_OIDC_ISSUER_URL
DEVOPS_TOOLS_AUTH_PROVIDER           DEVOPS_TOOLS_OIDC_CLIENT_ID
DEVOPS_TOOLS_PROMETHEUS_URL          DEVOPS_TOOLS_LOKI_URL
DEVOPS_TOOLS_DEPRECATED_HOSTS
```

`DEVOPS_TOOLS_CONFIG_DIR` is set in the Dockerfile as well as the chart. Miss it
and the container starts in `/home/nonroot`, where there is no configuration,
and exits.

### 3. The storage file

`config.storage.path` defaults to `/data/devops-tools.json`. Changing it points at
a file that does not exist yet, which reads as an empty portal. Either leave the
path alone, or rename the file on the volume in the same step.

### 4. Browser storage

`devops-tools.token`, `devops-tools.theme`, `devops-tools.sidebarCollapsed`,
`devops-tools.restartWatchers.v1`, and the `devops-tools:unauthorized` event. Renaming
these signs everyone out once and forgets their theme; nothing worse. They are
in `web/src/session.tsx` and `web/src/restartWatcher.ts`.

The Go module path, the chart directory and the image repository can be renamed
freely — none of them is read at runtime.

## The order that works

1. `fullnameOverride: devops-tools` (or `persistence.existingClaim`), and deploy
   that on its own, so the names are pinned before anything moves
2. Rename module, chart, image, environment variables and browser keys
3. Deploy, then check `kubectl get pvc` and that the users and roles are still
   there
4. Only then consider dropping the override — which renames the volume again,
   and needs the data copied across

Doing step 1 last is the way to discover all of this the hard way.
