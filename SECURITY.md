# Security Policy

## Reporting a vulnerability

Please do not open a public issue for security problems.

Report them privately through GitHub's ["Report a vulnerability"][advisory]
form, or by email to the maintainers. Include the affected version, what an
attacker can achieve, and the steps to reproduce it.

You can expect an acknowledgement within a few working days and an assessment
of the impact shortly after. Once a fix is available we will publish a release
and credit you, unless you prefer otherwise.

[advisory]: https://github.com/devops-tools/devops-tools/security/advisories/new

## Scope

DevOps Tools performs privileged operations in a Kubernetes cluster on behalf of
users, so the areas below are especially relevant:

- **Authorization bypass** — performing an operation, or reading a namespace,
  that the user's roles do not grant.
- **Authentication flaws** — forging or replaying session tokens, bypassing the
  login rate limit, or LDAP filter injection.
- **Secret exposure** — leaking secret values through logs, error messages or
  API responses to callers without `secret-read`.
- **Audit gaps** — a privileged action that leaves no audit record.

## Deployment guidance

A few things are the operator's responsibility rather than the application's:

- **Change the default local account.** A fresh install ships with
  `admin` / `admin` so it can be tried out immediately. It must be replaced
  before the service is reachable by anyone else.
- **Set `DEVOPS_TOOLS_SESSION_KEY`** and keep it secret. Without it each replica
  generates its own key at startup, so sessions break behind a load balancer.
- **Use LDAPS or StartTLS.** Plain `ldap://` is refused unless explicitly
  overridden, because credentials would otherwise cross the network in clear.
- **Restrict `/metrics`.** It is unauthenticated so Prometheus can scrape it;
  limit it at the ingress or with a network policy if that is a concern.
- **Scope the service account.** The chart's `ClusterRole` is the outer
  boundary of everything the tool can ever do. Use `rbac.scope: namespace` if a
  cluster-wide role is not acceptable.
- **Review `rbac.extraRules`.** Any API group you add there becomes something
  the tool may delete during a Helm uninstall.
