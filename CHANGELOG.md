# Changelog

What changed in each release, in the words of somebody deciding whether to
upgrade. The release notes on GitHub carry the same text.

## Unreleased

- **A failed Helm operation now says what went wrong.** Which resource could not
  be deleted is something the SDK mentions only in passing and leaves out of the
  error it returns, so the audit trail recorded "failed" and nothing more. It is
  carried out now — into the message on screen and into the trail.
- **`config.logLevel`**: debug, info, warn or error. Some of what the service
  knows was written at debug and therefore unreachable at any setting.
- **The chart now grants the service account every resource by default**
  (`rbac.fullAccess`). Removing a Helm release means deleting whatever that
  release created, and a list of kinds stops being enough the moment a chart
  carries an operator's custom resource — the uninstall halts half-way and says
  only that it failed. With `rbac.scope: cluster` this makes the service account
  a cluster administrator; it does not change what the portal offers anyone, but
  the token in the pod becomes a cluster-admin credential. Set
  `rbac.fullAccess: false` for the previous list of kinds. **An upgrade widens an
  existing installation's permissions**, which is worth knowing before taking it.
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
