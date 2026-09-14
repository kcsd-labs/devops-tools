# Changelog

What changed in each release, in the words of somebody deciding whether to
upgrade. The release notes on GitHub carry the same text.

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
