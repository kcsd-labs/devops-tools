# Changelog

What changed in each release, in the words of somebody deciding whether to
upgrade. The release notes on GitHub carry the same text.

## 0.25.7

- Everything Docker Scout reported against 0.25.6 is fixed. Go 1.26.9 — the
  image had been built with 1.26.8, which missed thirteen fixes to the standard
  library, three of them in the HTTP server and reachable before sign-in.
  Helm 3.22, the Kubernetes client libraries 0.37, and patched oras-go,
  golang.org/x/net, golang.org/x/crypto and go-ldap.
- Helm 3.22 no longer depends on containerd, and signs charts with
  ProtonMail's maintained OpenPGP library. The findings that had no fix
  upstream went with the code that carried them.
- One finding remains in scanners that read the module list rather than the
  code: GO-2026-5932, against golang.org/x/crypto, for openpgp packages that
  are not compiled into the binary. The image carries a VEX statement saying
  so, and CI fails the build if it ever stops being true.
- The base image is Debian 13. Debian 12 is past regular security support.
- The interface is built with Node 24, from the lock file, on Vite 8 and React
  Router 7. Until now the image ignored the lock file and built from whatever
  the version ranges resolved to that day.
- Images are published with an SBOM and build provenance.

## 0.25.6

- INFR-1802 — LDAP authentication: people sign in with the account they already
  have, and roles are granted in the portal rather than in a directory.
- INFR-1839 — the image is published to Docker Hub.
- INFR-1840 — the chart and the sources are published on GitHub.
- INFR-1861 — the vulnerabilities reported for the image on Docker Hub are
  fixed.
- INFR-1870 — an icon for the browser tab.
- INFR-1871 — example values files for local accounts, LDAP and OpenID Connect.
- INFR-1877 — an Audit page: who did what, in the namespaces you may see.
- INFR-1878 — backup and restore of the access model from the interface.

## 0.25.5

- **The chart now grants the service account every resource by default**
  (`rbac.fullAccess`). Removing a Helm release means deleting whatever that
  release created, and rolling one back means creating it again — a list of
  kinds stops being enough the moment a chart carries an operator's custom
  resource, and then the uninstall halts half-way, leaves the release behind
  and says only that it failed. Argo CD and Flux grant their controllers the
  same thing for the same reason. With `rbac.scope: cluster` this makes the
  service account a cluster administrator: it does not change what the portal
  offers anyone — that is the access model's decision — but the token in the
  pod becomes a cluster-admin credential. Set `rbac.fullAccess: false` for the
  previous list of kinds. **An upgrade widens an existing installation's
  permissions**, which is worth knowing before taking it.
- **A failed Helm operation now says what went wrong.** Which resource could not
  be deleted is something the SDK narrates in passing and leaves out of the
  error it returns, so the audit trail recorded "failed" and nothing else — and
  a half-removed release could sit unnoticed for a fortnight. That detail is
  carried out now, into the message on screen and into the trail.
- **`config.logLevel`**: debug, info, warn or error. Worth having because some
  of what the service knows was written at debug and therefore unreachable at
  any setting.
- The role editor reads as the three questions it asks. The buttons that add a
  namespace or a configuration path sat between two blocks, close enough to the
  next heading to look like part of it; they now continue the list they add to,
  and a rule separates the sections.
- The Storage tab no longer moves the Users / Roles / Storage switcher down a
  line when you reach it.
- The post-install notes said the default account was `admin` / `admin`. It has
  not been since the chart stopped shipping a password.

## 0.25.4

- **The access model moved into a Kubernetes Secret.** No volume, and more than
  one replica can now share it: the API server serialises the writes and a watch
  tells the other replicas at once. An existing installation is carried across on
  the first start and the file is left untouched, so the change can be undone.
  Set `config.storage.backend: file` to keep the old arrangement.
- **`persistence.enabled` now defaults to `false`**, and with it go the
  `storageClass` question, the one-replica limit and the few seconds of downtime
  that `strategy: Recreate` cost on every upgrade. Turn it back on for the file
  backend — the chart refuses that combination without a volume.
- **Backup and restore from the interface.** A new Storage tab under Access
  management says where the model lives and how large it is, hands back a
  snapshot, and puts one back. Behind a new global operation, `access-restore`.
- **An Audit page.** Who did what, read from Loki, or from the service's own pod
  logs where there is no Loki. Scoped by a new operation, `audit-read`: in a
  namespace it shows everyone's actions there, and globally it shows the entries
  that belong to no namespace — sign-ins, and who was granted what.
- Group names are stored once in a table rather than repeated for every member,
  and the document is compressed once it grows past 256 KB. Together these take
  the ceiling on how many people fit far out of sight.
- Sign-in times are written in one batch rather than one write per active person,
  and a lockout decided by one replica is honoured by all of them.

Upgrading from 0.25.3 in two steps: once with `persistence.enabled: true` so the
model is copied out of the file, then again without it. The chart stops an
upgrade that would skip the first step.

## 0.25.3

- Example values files for LDAP and for OpenID Connect, and install
  instructions that use them.
- An icon for the browser tab.

## 0.25.2

- Vulnerability scanning on a schedule, reporting only what is new.
