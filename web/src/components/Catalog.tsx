import { useEffect, useState } from "react";
import type { CatalogCheck, CatalogProblem, HelmRequest } from "../api";

// Installing from the catalog: the line that says what a check found, and the
// words a request is shown with. Shared by the Helm page, the install dialog
// and the administrators' request screen. No editor here: this is loaded with
// the Helm page, and the editor only with the dialog.

/** Something being waited for: a turning circle, and with `bar` a moving
    line under it. After a few seconds `slow` says why it takes a while. */
export function Loading({ text, bar, slow }: { text: string; bar?: boolean; slow?: string }) {
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (!slow) return;
    const t = setTimeout(() => setLate(true), 6000);
    return () => clearTimeout(t);
  }, [slow]);
  return (
    <div className="cat-loading" role="status" aria-live="polite">
      <div className="cat-loading-line">
        <span className="cat-spin" aria-hidden="true" />
        <span>{text}</span>
      </div>
      {bar && (
        <div className="cat-progress" aria-hidden="true">
          <span />
        </div>
      )}
      {late && slow && <div className="muted cat-loading-slow">{slow}</div>}
    </div>
  );
}

/** "StatefulSet ×1 · Service ×2", in the order the objects came. */
export function objectSummary(objects: { kind: string }[]): string {
  const counts = new Map<string, number>();
  for (const o of objects) counts.set(o.kind, (counts.get(o.kind) ?? 0) + 1);
  return Array.from(counts, ([k, n]) => (n > 1 ? `${k} ×${n}` : k)).join(", ");
}

/** What a check found, in a box: green when nothing stands in the way. */
export function CheckResult({
  check,
  checking,
  error,
  upgrade,
}: {
  check?: CatalogCheck;
  checking: boolean;
  error?: string;
  upgrade?: boolean;
}) {
  if (error) return <div className="ing-check err">⚠ {error}</div>;
  if (!check)
    return (
      <div className="ing-check muted-box">
        {checking ? (
          <span className="cat-loading-line">
            <span className="cat-spin" aria-hidden="true" />
            Checking the chart against the cluster…
          </span>
        ) : (
          "Not checked yet"
        )}
      </div>
    );
  const warns = check.problems.filter((p) => p.level === "warn");
  const found = check.imageStates.filter((s) => !s.missing && !s.unknown).length;
  return (
    <div className={"cat-check" + (checking ? " stale" : "")}>
      {check.ok ? (
        <div className={"ing-check " + (warns.length > 0 ? "warn" : "ok")}>
          {warns.length > 0 ? "⚠ Can be installed" : "✓ Check passed"} · {upgrade ? "the release will hold" : "will create"} {objectSummary(check.objects) || "nothing"}
          {check.imageStates.length > 0 && ` · ${found}/${check.imageStates.length} images found`}
          {warns.length > 0 && ` · ${warns.length} ${warns.length === 1 ? "warning" : "warnings"} below, for whoever approves`}
        </div>
      ) : (
        <div className="ing-check err">✕ This cannot be installed as it is</div>
      )}
      <ProblemList problems={check.problems} />
    </div>
  );
}

/** What a check found, errors first. */
export function ProblemList({ problems }: { problems: CatalogProblem[] }) {
  const sorted = [...problems.filter((p) => p.level === "error"), ...problems.filter((p) => p.level !== "error")];
  return (
    <>
      {sorted.map((p, i) => (
        <div key={i} className={"cat-problem " + p.level}>
          <span className={"ing-state " + (p.level === "error" ? "error" : "warn")}>{p.level === "error" ? "error" : "warning"}</span>
          {p.text}
        </div>
      ))}
    </>
  );
}

const STATUS: Record<HelmRequest["status"], [string, string]> = {
  waiting: ["Waiting for approval", "badge-warn"],
  installing: ["Installing…", "badge-muted"],
  installed: ["Installed", "badge-ok"],
  failed: ["Failed", "badge-err"],
  rejected: ["Rejected", "badge-err"],
  withdrawn: ["Withdrawn", "badge-muted"],
  expired: ["Expired", "badge-muted"],
};

export function RequestStatus({ r }: { r: HelmRequest }) {
  const [text, cls] = STATUS[r.status] ?? [r.status, "badge-muted"];
  return <span className={"badge " + cls}>{text}</span>;
}

/** "3 min ago", "2 h ago", "5 d ago". */
export function ago(iso?: string): string {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}

/** "in 6 d", for when a waiting request expires. */
export function until(iso: string): string {
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  if (s <= 0) return "now";
  if (s < 3600) return `in ${Math.max(1, Math.round(s / 60))} min`;
  if (s < 86400) return `in ${Math.round(s / 3600)} h`;
  return `in ${Math.round(s / 86400)} d`;
}

/** The line under a request's title: what, by whom, and how it went. */
export function requestLine(r: HelmRequest): string {
  const what =
    r.kind === "upgrade" ? `upgrade · ${r.chart} ${r.fromVersion ?? "?"} → ${r.version}` : `install · ${r.chart} ${r.version}`;
  return `${what} · ${r.repo}${r.newNamespace ? " · into a new namespace" : ""}`;
}

export function requestNote(r: HelmRequest): string {
  switch (r.status) {
    case "waiting":
      return `${r.requester} · ${ago(r.created)} · expires ${until(r.expires)}`;
    case "installing":
      return `approved by ${r.decider} · ${ago(r.decided)}`;
    case "installed":
      return `${r.decider === r.requester ? "installed by" : "approved by"} ${r.decider} · ${ago(r.decided)}${r.askedValues ? " · values changed on approval" : ""}${r.grantedRoles?.length ? ` · access given to ${r.grantedRoles.join(", ")}` : ""}`;
    case "failed":
      return r.detail ?? "the install failed";
    case "rejected":
      return `${r.decider}: “${r.comment ?? ""}”`;
    case "withdrawn":
      return `withdrawn by ${r.decider} · ${ago(r.decided)}`;
    case "expired":
      return "nobody answered in time";
  }
}
