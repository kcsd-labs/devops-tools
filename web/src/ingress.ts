import type { IngressForm, IngressProblem, IngressRoute } from "./api";

// Readable names for the annotations people set most. Labels only: any other
// annotation is written as it is, and none of these is required or refused.
const NGINX = "nginx.ingress.kubernetes.io/";
export const SETTING_LABELS: Record<string, { label: string; hint?: string }> = {
  [NGINX + "proxy-read-timeout"]: { label: "Read timeout", hint: "seconds, e.g. 3600" },
  [NGINX + "proxy-send-timeout"]: { label: "Send timeout", hint: "seconds" },
  [NGINX + "proxy-connect-timeout"]: { label: "Connect timeout", hint: "seconds" },
  [NGINX + "proxy-body-size"]: { label: "Max request size", hint: "e.g. 50m" },
  [NGINX + "proxy-buffering"]: { label: "Buffering", hint: "on or off" },
  [NGINX + "rewrite-target"]: { label: "Rewrite path to", hint: "e.g. /$2" },
  [NGINX + "ssl-redirect"]: { label: "Redirect to HTTPS", hint: "true or false" },
  [NGINX + "force-ssl-redirect"]: { label: "Always redirect to HTTPS", hint: "true or false" },
  [NGINX + "backend-protocol"]: { label: "Backend protocol", hint: "HTTP, HTTPS, GRPC" },
  [NGINX + "enable-cors"]: { label: "CORS", hint: "true or false" },
  [NGINX + "cors-allow-origin"]: { label: "CORS origins" },
  [NGINX + "whitelist-source-range"]: { label: "Allowed client addresses", hint: "CIDRs, comma separated" },
  [NGINX + "affinity"]: { label: "Sticky sessions", hint: "cookie" },
  [NGINX + "configuration-snippet"]: { label: "Location snippet" },
  [NGINX + "server-snippet"]: { label: "Server snippet" },
};

/** A note to show beside a setting, when there is one worth knowing. */
export function settingNote(key: string): string {
  if (key.endsWith("-snippet")) return "Runs only if the ingress controller allows snippets";
  return "";
}

/** The worst problem first: errors, then warnings. */
export function worst(problems: IngressProblem[]): IngressProblem | undefined {
  return problems.find((p) => p.level === "error") ?? problems[0];
}

/** One line saying what a check found, for the line above Save. */
export function checkSummary(routes: IngressRoute[]): { ok: boolean; text: string } {
  const problems = routes.flatMap((r) => r.problems);
  if (problems.length > 0) {
    const seen = new Set<string>();
    const texts = problems.map((p) => p.text).filter((t) => !seen.has(t) && seen.add(t));
    return { ok: false, text: texts.join(" · ") };
  }
  const parts: string[] = [];
  const r = routes[0];
  if (r?.host) parts.push("address is free");
  if (r?.ready) parts.push(`${r.ready.ready}/${r.ready.total} pods ready`);
  if (r?.tlsSecret) parts.push("certificate covers the host");
  return { ok: true, text: parts.length ? capital(parts.join(" · ")) : "Nothing stands in the way" };
}

function capital(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** A form with nothing in it. */
export function emptyForm(): IngressForm {
  return { class: "", rules: [], tls: [], annotations: {} };
}

/** The address a route is opened at. */
export function routeURL(r: { host: string; path: string; tlsSecret?: string }): string {
  return (r.tlsSecret ? "https://" : "http://") + r.host + (r.path === "/" ? "" : r.path);
}
